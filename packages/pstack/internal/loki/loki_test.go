package loki

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// Internal tests (package loki): they pin now, geteuid and chown and call render. None runs in
// parallel, because the seams are package vars.

const testSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

// s3Settings is a valid S3 save: AWS, virtual-hosted, cutover 2026-09-16. A fresh pointer each call.
func s3Settings() Settings {
	s := Defaults()
	s.Storage = Storage{Type: StorageS3, S3: &S3{
		Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: "pstack-logs",
		AccessKeyID: "AKIAIOSFODNN7EXAMPLE", Cutover: "2026-09-16",
	}}
	return s
}

// pin sets the package clock for one test.
func pin(t *testing.T, at time.Time) {
	t.Helper()
	was := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = was })
}

// asEuid pins the euid and records every chown as "path uid gid".
func asEuid(t *testing.T, euid int) *[]string {
	t.Helper()
	wasEuid, wasChown := geteuid, chown
	calls := []string{}
	geteuid = func() int { return euid }
	chown = func(path string, uid, gid int) error {
		calls = append(calls, fmt.Sprintf("%s %d %d", path, uid, gid))
		return nil
	}
	t.Cleanup(func() { geteuid, chown = wasEuid, wasChown })
	return &calls
}

func TestJSON(t *testing.T) {
	t.Run("settings marshal in spec order, s3 null on filesystem", func(t *testing.T) {
		// negative control: add omitempty to Storage.S3's tag — `"s3":null` disappears.
		for _, c := range []struct {
			in   any
			want string
		}{
			{Defaults(), `{"retentionDays":7,"chunks":{"idlePeriodMinutes":30,"maxAgeMinutes":120,"targetSizeKiB":1536,"encoding":"snappy"},"storage":{"type":"filesystem","s3":null}}`},
			{s3Settings().Storage, `{"type":"s3","s3":{"endpoint":"https://s3.eu-central-1.amazonaws.com","region":"eu-central-1","bucket":"pstack-logs","pathStyle":false,"accessKeyId":"AKIAIOSFODNN7EXAMPLE","cutover":"2026-09-16"}}`},
		} {
			if got := string(jsonx.Must(c.in)); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		}
	})

	t.Run("limits marshal in spec order, encodings an array", func(t *testing.T) {
		// negative control: set l.Encodings = nil in LimitsAt — `"encodings":null`.
		noon := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
		want := `{"retentionDays":{"min":1,"max":365},"idlePeriodMinutes":{"min":5,"max":60},"maxAgeMinutes":{"min":30,"max":180},"targetSizeKiB":{"min":512,"max":1536},"encodings":["snappy","gzip","lz4","zstd"],"earliestCutover":"2026-09-16"}`
		if got := string(jsonx.Must(LimitsAt(noon, Lead(5*time.Minute)))); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	})

	t.Run("a caller editing its limits does not edit what Validate reads", func(t *testing.T) {
		// negative control: drop slices.Clone in LimitsAt — "snappy" stops validating.
		LimitsAt(time.Now(), 0).Encodings[0] = "none"
		if err := Validate(Defaults(), "", false, 0); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEarliestCutover(t *testing.T) {
	t.Run("Lead is the ready timeout plus 10m", func(t *testing.T) {
		// negative control: return readyTimeout from Lead — 5m, not 15m.
		if got := Lead(5 * time.Minute); got != 15*time.Minute {
			t.Fatalf("got %s", got)
		}
	})

	lead := Lead(5 * time.Minute) // 15m, so the window is 2×15m + 10m = 40m
	for _, c := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"noon UTC: tomorrow", time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC), "2026-09-16"},
		{"23:40 UTC: the day after", time.Date(2026, 9, 15, 23, 40, 0, 0, time.UTC), "2026-09-17"},
		{"23:30 UTC: the window is 2×lead + 10m, not lead + 10m", time.Date(2026, 9, 15, 23, 30, 0, 0, time.UTC), "2026-09-17"},
		{"23:20 UTC: a window ending at midnight is not strictly before it", time.Date(2026, 9, 15, 23, 20, 0, 0, time.UTC), "2026-09-17"},
		{"a -05:00 clock is read in UTC", time.Date(2026, 9, 15, 20, 0, 0, 0, time.FixedZone("EST", -5*3600)), "2026-09-17"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// negative control, per row: drop AddDate (noon → 2026-09-15); use lead for 2*lead (23:30 → 2026-09-16);
			// add the day only when the window's end is past its midnight (23:20 → 2026-09-16); drop .UTC() (-05:00 → 2026-09-16).
			if got := EarliestCutover(c.at, lead); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	t.Run("*Error is a 400, wrapped or not; the 409 sentinels are not", func(t *testing.T) {
		// negative control: declare ErrNeedsRoot as &Error{"the S3 credentials file needs root"} — IsError reports it.
		if !IsError(&Error{"x"}) || !IsError(fmt.Errorf("render: %w", &Error{"x"})) {
			t.Error("a *Error is not recognised")
		}
		for _, err := range []error{ErrOneWay, ErrFixed, ErrNeedsRoot, errors.New("x"), nil} {
			if IsError(err) {
				t.Errorf("%v reads as a 400", err)
			}
		}
	})
}

func TestValidate(t *testing.T) {
	pin(t, time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	lead := Lead(5 * time.Minute)

	// refused asserts err is a *Error with exactly msg.
	refused := func(t *testing.T, what string, err error, msg string) {
		t.Helper()
		if !IsError(err) || err.Error() != msg {
			t.Errorf("%s: got %v, want %q", what, err, msg)
		}
	}
	// withS3 is s3Settings with one change.
	withS3 := func(edit func(*S3)) Settings {
		s := s3Settings()
		edit(s.Storage.S3)
		return s
	}

	t.Run("every range at min-1, min, max and max+1", func(t *testing.T) {
		// negative control: `f.v > f.r.Max` → `f.v >= f.r.Max` in check — every max is refused.
		for _, c := range []struct {
			field string
			r     Range
			set   func(*Settings, int)
		}{
			{"retentionDays", Range{1, 365}, func(s *Settings, v int) { s.RetentionDays = v }},
			{"chunks.idlePeriodMinutes", Range{5, 60}, func(s *Settings, v int) { s.Chunks.IdlePeriodMinutes, s.Chunks.MaxAgeMinutes = v, 180 }},
			{"chunks.maxAgeMinutes", Range{30, 180}, func(s *Settings, v int) { s.Chunks.MaxAgeMinutes, s.Chunks.IdlePeriodMinutes = v, 5 }},
			{"chunks.targetSizeKiB", Range{512, 1536}, func(s *Settings, v int) { s.Chunks.TargetSizeKiB = v }},
		} {
			for _, v := range []int{c.r.Min - 1, c.r.Min, c.r.Max, c.r.Max + 1} {
				s := Defaults()
				c.set(&s, v)
				err := Validate(s, "", false, lead)
				if v >= c.r.Min && v <= c.r.Max {
					if err != nil {
						t.Errorf("%s=%d: %v", c.field, v, err)
					}
					continue
				}
				refused(t, fmt.Sprintf("%s=%d", c.field, v), err, fmt.Sprintf("%s must be %d–%d", c.field, c.r.Min, c.r.Max))
			}
		}
	})

	t.Run("idle above the max age is refused, equal is not", func(t *testing.T) {
		// negative control: drop the IdlePeriodMinutes > MaxAgeMinutes check — 31/30 is accepted.
		s := Defaults()
		s.Chunks.IdlePeriodMinutes, s.Chunks.MaxAgeMinutes = 30, 30
		if err := Validate(s, "", false, lead); err != nil {
			t.Fatal(err)
		}
		s.Chunks.IdlePeriodMinutes = 31
		refused(t, "31/30", Validate(s, "", false, lead), "chunks.idlePeriodMinutes must not exceed chunks.maxAgeMinutes")
	})

	t.Run("encodings: the four and nothing else", func(t *testing.T) {
		// negative control: add "none" to bounds.Encodings — it is accepted.
		for _, e := range []string{"snappy", "gzip", "lz4", "zstd"} {
			s := Defaults()
			s.Chunks.Encoding = e
			if err := Validate(s, "", false, lead); err != nil {
				t.Errorf("%s: %v", e, err)
			}
		}
		for _, e := range []string{"none", "lz4-1M", "SNAPPY", ""} {
			s := Defaults()
			s.Chunks.Encoding = e
			refused(t, e, Validate(s, "", false, lead), "chunks.encoding must be one of snappy, gzip, lz4, zstd")
		}
	})

	t.Run("storage type and shape", func(t *testing.T) {
		// negative control: return nil from check's default case — type "gcs" is accepted.
		gcs := Defaults()
		gcs.Storage.Type = "gcs"
		fsWithS3 := s3Settings()
		fsWithS3.Storage.Type = StorageFilesystem
		s3NoFields := Defaults()
		s3NoFields.Storage.Type = StorageS3
		refused(t, "gcs", Validate(gcs, "", false, lead), "type must be filesystem or s3")
		refused(t, "filesystem with s3", Validate(fsWithS3, "", false, lead), "type filesystem takes no s3 fields")
		refused(t, "s3 without s3", Validate(s3NoFields, testSecret, true, lead), "type s3 needs its s3 fields")
		if err := Validate(s3Settings(), testSecret, true, lead); err != nil {
			t.Fatalf("the valid S3 save: %v", err)
		}
	})

	t.Run("endpoint: http(s)://host[:port], nothing else", func(t *testing.T) {
		// negative control: drop `(u.Path != "" && u.Path != "/")` from endpoint — the path case is accepted.
		for _, e := range []string{"https://s3.eu-central-1.amazonaws.com", "http://minio:9000", "http://10.0.0.5:9000/"} {
			if err := Validate(withS3(func(c *S3) { c.Endpoint = e }), testSecret, true, lead); err != nil {
				t.Errorf("%s: %v", e, err)
			}
		}
		for _, e := range []string{
			"s3.eu-central-1.amazonaws.com",           // no scheme
			"https://s3.amazonaws.com/pstack-logs",    // a path
			"https://AKIA:secret@s3.amazonaws.com",    // userinfo
			"ftp://s3.amazonaws.com",                  // scheme
			"https://s3.amazonaws.com?x=1",            // query
			"https://s3.amazonaws.com#",               // an empty fragment
			"https://[::1]:9000",                      // unquotable in YAML
			"https://minio:",                          // an empty port
			"https://",                                // no host
			"https://s3.amazonaws.com\n  insecure: x", // injection
		} {
			refused(t, e, Validate(withS3(func(c *S3) { c.Endpoint = e }), testSecret, true, lead), "endpoint must be http(s)://host[:port]")
		}
	})

	t.Run("region", func(t *testing.T) {
		// negative control: regionRe `^[a-z0-9-]{1,32}$` → `^[A-Za-z0-9_-]{1,32}$` — "eu_central" is accepted.
		for _, r := range []string{"", "EU", "eu_central", strings.Repeat("a", 33)} {
			refused(t, r, Validate(withS3(func(c *S3) { c.Region = r }), testSecret, true, lead), "region must be 1–32 characters of a-z, 0-9 and -")
		}
		if err := Validate(withS3(func(c *S3) { c.Region = "garage" }), testSecret, true, lead); err != nil {
			t.Error(err)
		}
	})

	t.Run("bucket: S3 naming without a comma; a dot only path-style", func(t *testing.T) {
		// negative control: drop the `strings.Contains(c.Bucket, ".") && !c.PathStyle` check — "logs.v1" is accepted virtual-hosted.
		for _, b := range []string{"ab", "pstack,logs", "-logs", "logs-", "Logs", strings.Repeat("a", 64)} {
			refused(t, b, Validate(withS3(func(c *S3) { c.Bucket = b }), testSecret, true, lead),
				"bucket must be 3–63 characters of a-z, 0-9, - and ., starting and ending with a letter or digit")
		}
		refused(t, "logs.v1", Validate(withS3(func(c *S3) { c.Bucket = "logs.v1" }), testSecret, true, lead), "bucket may contain . only with pathStyle")
		if err := Validate(withS3(func(c *S3) { c.Bucket, c.PathStyle = "logs.v1", true }), testSecret, true, lead); err != nil {
			t.Error(err)
		}
	})

	t.Run("accessKeyId and secret: the INI charset, 1–256 and 8–256 bytes", func(t *testing.T) {
		// negative control: iniValue(secret, 7) in Validate — the 7-byte secret is accepted.
		for _, id := range []string{"", "AKIA EXAMPLE", "AKIA#1", "AKIA;1", "AKIAé", strings.Repeat("A", 257)} {
			refused(t, id, Validate(withS3(func(c *S3) { c.AccessKeyID = id }), testSecret, true, lead), "accessKeyId must be 1–256 printable ASCII characters, no space, # or ;")
		}
		for _, secret := range []string{"", "1234567", "with space!", "tab\tsecret", strings.Repeat("s", 257)} {
			refused(t, fmt.Sprintf("%q", secret), Validate(s3Settings(), secret, true, lead), "secretAccessKey must be 8–256 printable ASCII characters, no space, # or ;")
		}
		for _, secret := range []string{"12345678", strings.Repeat("s", 256)} {
			if err := Validate(s3Settings(), secret, true, lead); err != nil {
				t.Errorf("%d bytes: %v", len(secret), err)
			}
		}
		if err := Validate(withS3(func(c *S3) { c.AccessKeyID = "A" }), testSecret, true, lead); err != nil {
			t.Error(err)
		}
	})

	t.Run("cutover is a real YYYY-MM-DD", func(t *testing.T) {
		// negative control: drop the time.Parse check in check — "2026-02-30" is accepted.
		for _, d := range []string{"", "2026-9-16", "16-09-2026", "2026-02-30", "2026-09-16T00:00:00Z"} {
			refused(t, d, Validate(withS3(func(c *S3) { c.Cutover = d }), testSecret, false, lead), "cutover must be YYYY-MM-DD")
		}
	})

	t.Run("adding S3: a cutover before earliestCutover is refused, equal accepted", func(t *testing.T) {
		// negative control: `Cutover < earliest` → `Cutover <= earliest` in Validate — 2026-09-16 is refused.
		refused(t, "2026-09-15", Validate(withS3(func(c *S3) { c.Cutover = "2026-09-15" }), testSecret, true, lead), "cutover must be 2026-09-16 or later")
		if err := Validate(withS3(func(c *S3) { c.Cutover = "2026-09-16" }), testSecret, true, lead); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("an S3 host rotating keys after its cutover is not held to the floor", func(t *testing.T) {
		// negative control: drop `addingS3 &&` from Validate — the rotation is refused.
		if err := Validate(withS3(func(c *S3) { c.Cutover = "2026-01-01" }), testSecret, false, lead); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCredentials(t *testing.T) {
	t.Run("exact bytes", func(t *testing.T) {
		// negative control: drop the final "\n" from Credentials — the bytes differ.
		want := "[default]\naws_access_key_id = AKIAIOSFODNN7EXAMPLE\naws_secret_access_key = " + testSecret + "\n"
		if got := Credentials("AKIAIOSFODNN7EXAMPLE", testSecret); got != want {
			t.Errorf("got %q", got)
		}
	})

	t.Run("CredentialsOwner: root or Loki's own uid, nobody else", func(t *testing.T) {
		// negative control: return nil at the end of CredentialsOwner — euid 1000 is not refused.
		for _, c := range []struct {
			euid int
			want error
		}{{0, nil}, {10001, nil}, {1000, ErrNeedsRoot}} {
			asEuid(t, c.euid)
			if err := CredentialsOwner(10001); !errors.Is(err, c.want) || (c.want == nil && err != nil) {
				t.Errorf("euid %d: got %v", c.euid, err)
			}
		}
	})

	t.Run("root writes 0600 and chowns it to Loki's uid", func(t *testing.T) {
		// negative control: drop the chown call from WriteFile — nothing is recorded.
		calls := asEuid(t, 0)
		p := filepath.Join(t.TempDir(), CredentialsFile+NextSuffix)
		body := Credentials("AKIAIOSFODNN7EXAMPLE", testSecret)
		if err := WriteFile(p, body, 0o600, 10001); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(*calls, ","); got != p+" 10001 10001" {
			t.Errorf("chown calls %q", got)
		}
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, %v", st, err)
		}
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Errorf("body %q", got)
		}
	})

	t.Run("Loki's own uid writes as is, and config.yaml is never chowned", func(t *testing.T) {
		// negative control: drop `geteuid() == 0 &&` from WriteFile's chown condition — the euid-10001 write chowns.
		dir := t.TempDir()
		calls := asEuid(t, 10001)
		if err := WriteFile(filepath.Join(dir, CredentialsFile), "x", 0o600, 10001); err != nil {
			t.Fatal(err)
		}
		rootCalls := asEuid(t, 0)
		if err := WriteFile(filepath.Join(dir, ConfigFile), "x", 0o644, 10001); err != nil {
			t.Fatal(err)
		}
		if len(*calls)+len(*rootCalls) != 0 {
			t.Errorf("chowned: %v %v", *calls, *rootCalls)
		}
		if st, _ := os.Stat(filepath.Join(dir, ConfigFile)); st.Mode().Perm() != 0o644 {
			t.Errorf("config.yaml mode %v", st.Mode())
		}
	})

	t.Run("any other euid is refused before a byte is written", func(t *testing.T) {
		// negative control: drop the CredentialsOwner check from WriteFile — the file is written.
		asEuid(t, 1000)
		p := filepath.Join(t.TempDir(), CredentialsFile)
		if err := WriteFile(p, "x", 0o600, 10001); !errors.Is(err, ErrNeedsRoot) {
			t.Fatalf("got %v", err)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("the file exists: %v", err)
		}
	})

	t.Run("a leftover symlink is replaced, never written through", func(t *testing.T) {
		// negative control: drop the os.Remove from WriteFile — the secret lands in the link's target.
		asEuid(t, 10001)
		dir := t.TempDir()
		target := filepath.Join(dir, "elsewhere")
		if err := os.WriteFile(target, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, CredentialsFile+NextSuffix)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		if err := WriteFile(p, testSecret, 0o600, 10001); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(target); len(got) != 0 {
			t.Errorf("the target holds %q", got)
		}
		if st, err := os.Lstat(p); err != nil || !st.Mode().IsRegular() {
			t.Errorf("%s is not a regular file: %v", p, err)
		}
	})
}

func TestWritable(t *testing.T) {
	t.Run("config.yaml present and the directory writable", func(t *testing.T) {
		// negative control: drop the config.yaml stat from Writable — the empty directory reads as writable.
		dir := t.TempDir()
		if Writable(dir) || Writable(filepath.Join(dir, "absent")) {
			t.Error("a directory without config.yaml is writable")
		}
		if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !Writable(dir) {
			t.Error("not writable")
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("the probe was left behind: %v", entries)
		}
	})

	t.Run("a read-only mount is not writable", func(t *testing.T) {
		// negative control: return true right after the stat in Writable — the 0555 directory reads as writable.
		if os.Geteuid() == 0 {
			t.Skip("root ignores the directory mode this test needs")
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		if Writable(dir) {
			t.Error("a 0555 directory is writable")
		}
	})
}

func TestDir(t *testing.T) {
	t.Run("PSTACK_LOKI_DIR by presence, then /etc/loki, then <data>/control/loki", func(t *testing.T) {
		// negative control: `if d := os.Getenv("PSTACK_LOKI_DIR"); d != ""` in Dir — the empty value falls through.
		t.Setenv("PSTACK_LOKI_DIR", "/srv/loki")
		if got := Dir("/data"); got != "/srv/loki" {
			t.Errorf("set: %q", got)
		}
		t.Setenv("PSTACK_LOKI_DIR", "")
		if got := Dir("/data"); got != "" {
			t.Errorf("empty: %q", got)
		}
		os.Unsetenv("PSTACK_LOKI_DIR")
		if st, err := os.Stat(lokiMount); err == nil && st.IsDir() {
			t.Skip("/etc/loki exists on this machine")
		}
		if got := Dir("/data"); got != filepath.Join("/data", "control", "loki") {
			t.Errorf("unset: %q", got)
		}
	})
}

// ── the row, Merge, Changed (T4) ─────────────────────────────────────────────────────────────────

const (
	rowLead   = 15 * time.Minute // Lead(5m): the default ready timeout's lead
	rowKeyID  = "AKIAOLDKEY01"
	rowSecret = "old-secret-0001"
)

// rowNoon is the day before patchS3's cutover: EarliestCutover(rowNoon, rowLead) is 2026-09-16.
var rowNoon = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

func openRowStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func readRow(t *testing.T, st *store.Store) *Row {
	t.Helper()
	r, err := Read(st)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// patchS3 is a complete, valid S3 storage save. Every call returns fresh pointers.
func patchS3(bucket string) *StoragePatch {
	return &StoragePatch{
		Storage: Storage{Type: StorageS3, S3: &S3{
			Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1", Bucket: bucket,
			AccessKeyID: rowKeyID, Cutover: "2026-09-16",
		}},
		Secret: rowSecret,
	}
}

// s3Row is a stored S3 row: the defaults plus patchS3("pstack-logs").
func s3Row() *Row {
	s := Defaults()
	s.Storage = patchS3("pstack-logs").Storage
	return &Row{Settings: s, Secret: rowSecret}
}

func TestRow(t *testing.T) {
	t.Run("an empty table reads as nil", func(t *testing.T) {
		// negative control: return &Row{Settings: Defaults()}, nil on sql.ErrNoRows → Read is non-nil and this fails
		if r := readRow(t, openRowStore(t)); r != nil {
			t.Fatalf("an empty table read as %+v", r)
		}
	})

	t.Run("a save over an empty table is in flight with no Previous, and Revert empties it again", func(t *testing.T) {
		// negative control: delete Revert's DELETE statement → the first save survives the revert and the last Read is non-nil
		st := openRowStore(t)
		s := s3Row().Settings
		if err := Save(st, s, rowSecret, nil); err != nil {
			t.Fatal(err)
		}
		r := readRow(t, st)
		if r == nil || !r.InFlight || r.Previous != nil {
			t.Fatalf("after a first save: %+v", r)
		}
		if !reflect.DeepEqual(r.Settings, s) || r.Secret != rowSecret || r.UpdatedAt == 0 {
			t.Fatalf("saved %+v / %q / %d", r.Settings, r.Secret, r.UpdatedAt)
		}
		if err := Revert(st); err != nil {
			t.Fatal(err)
		}
		if r := readRow(t, st); r != nil {
			t.Fatalf("reverting a first save left %+v", r)
		}
	})

	t.Run("a save over a stored row carries it as Previous, and Revert restores it", func(t *testing.T) {
		// negative control: make Revert's UPDATE set config = config, secret = secret → the save survives and storage reads s3
		st := openRowStore(t)
		first := Defaults()
		first.RetentionDays = 14
		if err := Save(st, first, "", nil); err != nil {
			t.Fatal(err)
		}
		if err := Finish(st); err != nil {
			t.Fatal(err)
		}
		stored := readRow(t, st)
		second := s3Row().Settings
		if err := Save(st, second, rowSecret, stored); err != nil {
			t.Fatal(err)
		}
		inFlight := readRow(t, st)
		if inFlight == nil || !inFlight.InFlight || inFlight.Previous == nil ||
			!reflect.DeepEqual(inFlight.Previous.Settings, first) || inFlight.Previous.Secret != "" ||
			!reflect.DeepEqual(inFlight.Settings, second) || inFlight.Secret != rowSecret {
			t.Fatalf("in flight: %+v", inFlight)
		}
		if err := Revert(st); err != nil {
			t.Fatal(err)
		}
		back := readRow(t, st)
		if back == nil || back.InFlight || back.Previous != nil || !reflect.DeepEqual(back.Settings, first) || back.Secret != "" {
			t.Fatalf("after Revert: %+v", back)
		}
	})

	t.Run("Finish keeps the save and clears previous_*; a Revert after it changes nothing", func(t *testing.T) {
		// negative control: make Finish set only previous_secret = NULL → previous_config stays '' and InFlight stays true
		st := openRowStore(t)
		s := s3Row().Settings
		if err := Save(st, s, rowSecret, nil); err != nil {
			t.Fatal(err)
		}
		if err := Finish(st); err != nil {
			t.Fatal(err)
		}
		done := readRow(t, st)
		if done == nil || done.InFlight || done.Previous != nil || !reflect.DeepEqual(done.Settings, s) || done.Secret != rowSecret {
			t.Fatalf("after Finish: %+v", done)
		}
		if err := Revert(st); err != nil {
			t.Fatal(err)
		}
		if again := readRow(t, st); !reflect.DeepEqual(again, done) {
			t.Fatalf("Revert with no apply in flight changed the row: %+v → %+v", done, again)
		}
	})
}

func TestMerge(t *testing.T) {
	t.Run("a nil row merges onto Defaults()", func(t *testing.T) {
		// negative control: start Merge from Settings{} when row is nil → Validate refuses retentionDays 0 and the empty storage type
		c := &ChunksPatch{RetentionDays: 14, Chunks: Chunks{IdlePeriodMinutes: 15, MaxAgeMinutes: 60, TargetSizeKiB: 1024, Encoding: "zstd"}}
		got, secret, err := Merge(nil, c, nil, rowLead)
		want := Defaults()
		want.RetentionDays, want.Chunks = c.RetentionDays, c.Chunks
		if err != nil || secret != "" || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v %q %v, want %+v", got, secret, err, want)
		}
	})

	t.Run("a chunks patch keeps the stored storage and secret", func(t *testing.T) {
		// negative control: start Merge from Defaults() even when a row is given → storage reads filesystem and the secret ""
		row := s3Row()
		got, secret, err := Merge(row, &ChunksPatch{RetentionDays: 30, Chunks: row.Settings.Chunks}, nil, rowLead)
		if err != nil || secret != rowSecret || got.RetentionDays != 30 || !reflect.DeepEqual(got.Storage, row.Settings.Storage) {
			t.Fatalf("got %+v %q %v", got, secret, err)
		}
	})

	t.Run("filesystem to S3 needs every field and a secret", func(t *testing.T) {
		// negative control: return merged without calling Validate → the patch with no region merges
		pin(t, rowNoon)
		row := &Row{Settings: Defaults()}
		got, secret, err := Merge(row, nil, patchS3("pstack-logs"), rowLead)
		if err != nil || secret != rowSecret || !reflect.DeepEqual(got.Storage, patchS3("pstack-logs").Storage) {
			t.Fatalf("a complete S3 add: %+v %q %v", got, secret, err)
		}
		noRegion := patchS3("pstack-logs")
		noRegion.Storage.S3.Region = ""
		if _, _, err := Merge(row, nil, noRegion, rowLead); !IsError(err) || !strings.Contains(err.Error(), "region") {
			t.Fatalf("no region: %v", err)
		}
		noSecret := patchS3("pstack-logs")
		noSecret.Secret = ""
		if _, _, err := Merge(row, nil, noSecret, rowLead); !IsError(err) {
			t.Fatalf("no secret: %v", err)
		}
		keep := patchS3("pstack-logs")
		keep.Secret, keep.KeepSecret = "", true
		if _, _, err := Merge(row, nil, keep, rowLead); !IsError(err) || !strings.Contains(err.Error(), "secretAccessKey is required") {
			t.Fatalf("keepSecret with nothing stored: %v", err)
		}
	})

	t.Run("only the save that adds S3 checks the cutover against earliestCutover", func(t *testing.T) {
		// negative control: compute addingS3 as merged.Storage.Type == StorageS3 → the rotation on a host past its cutover is refused
		pin(t, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
		if _, _, err := Merge(&Row{Settings: Defaults()}, nil, patchS3("pstack-logs"), rowLead); !IsError(err) || !strings.Contains(err.Error(), "cutover") {
			t.Fatalf("an S3 add with a past cutover: %v", err)
		}
		rotate := patchS3("pstack-logs")
		rotate.Secret = "new-secret-0002"
		if _, _, err := Merge(s3Row(), nil, rotate, rowLead); err != nil {
			t.Fatalf("a rotation on a host past its cutover: %v", err)
		}
	})

	t.Run("S3 to filesystem is ErrOneWay, before the cutover too", func(t *testing.T) {
		// negative control: delete the ErrOneWay case → the filesystem patch reaches Validate and merges
		pin(t, rowNoon) // the stored cutover, 2026-09-16, has not come
		_, _, err := Merge(s3Row(), nil, &StoragePatch{Storage: Storage{Type: StorageFilesystem}}, rowLead)
		if !errors.Is(err, ErrOneWay) || IsError(err) {
			t.Fatalf("S3 → filesystem: %v (a *Error would be a 400)", err)
		}
	})

	t.Run("a changed endpoint, region, bucket, pathStyle or cutover is ErrFixed", func(t *testing.T) {
		// negative control: delete the ErrFixed case → every changed patch merges
		for name, change := range map[string]func(*S3){
			"endpoint":  func(s *S3) { s.Endpoint = "https://minio.internal:9000" },
			"region":    func(s *S3) { s.Region = "us-east-1" },
			"bucket":    func(s *S3) { s.Bucket = "other-logs" },
			"pathStyle": func(s *S3) { s.PathStyle = true },
			"cutover":   func(s *S3) { s.Cutover = "2026-09-17" },
		} {
			p := patchS3("pstack-logs")
			change(p.Storage.S3)
			if _, _, err := Merge(s3Row(), nil, p, rowLead); !errors.Is(err, ErrFixed) || IsError(err) {
				t.Errorf("changed %s: %v", name, err)
			}
		}
	})

	t.Run("credentials rotate, and KeepSecret keeps the stored secret", func(t *testing.T) {
		// negative control: resolve KeepSecret to sp.Secret → the kept secret comes back "" and Validate refuses it
		rotate := patchS3("pstack-logs")
		rotate.Storage.S3.AccessKeyID, rotate.Secret = "AKIANEWKEY02", "new-secret-0002"
		got, secret, err := Merge(s3Row(), nil, rotate, rowLead)
		if err != nil || secret != "new-secret-0002" || got.Storage.S3.AccessKeyID != "AKIANEWKEY02" {
			t.Fatalf("rotation: %+v %q %v", got.Storage.S3, secret, err)
		}
		keep := patchS3("pstack-logs")
		keep.Secret, keep.KeepSecret = "", true
		if _, secret, err := Merge(s3Row(), nil, keep, rowLead); err != nil || secret != rowSecret {
			t.Fatalf("keepSecret: %q %v", secret, err)
		}
	})

	t.Run("KeepSecret with a changed accessKeyId is refused", func(t *testing.T) {
		// negative control: drop the key-id comparison from the KeepSecret case → the old secret is kept under the new key id
		keep := patchS3("pstack-logs")
		keep.Storage.S3.AccessKeyID = "AKIANEWKEY02"
		keep.Secret, keep.KeepSecret = "", true
		if _, _, err := Merge(s3Row(), nil, keep, rowLead); !IsError(err) || !strings.Contains(err.Error(), "secretAccessKey is required") {
			t.Fatalf("a new key id with the old secret: %v", err)
		}
	})

	t.Run("a filesystem patch on a filesystem host changes nothing", func(t *testing.T) {
		// negative control: delete the filesystem-to-filesystem case → KeepSecret finds no stored secret and refuses
		row := &Row{Settings: Defaults()}
		row.Settings.RetentionDays = 14
		got, secret, err := Merge(row, nil, &StoragePatch{Storage: Storage{Type: StorageFilesystem}, KeepSecret: true}, rowLead)
		if err != nil || secret != "" || !reflect.DeepEqual(got, row.Settings) {
			t.Fatalf("got %+v %q %v", got, secret, err)
		}
	})

	t.Run("two queued storage saves with different buckets: the second is ErrFixed once the first is stored", func(t *testing.T) {
		// negative control: delete the ErrFixed case → the second bucket merges over the first
		pin(t, rowNoon)
		st := openRowStore(t)
		fs := readRow(t, st) // nil: the empty table is slice 1's filesystem config
		a, b := patchS3("pstack-logs"), patchS3("other-logs")
		mergedA, secretA, err := Merge(fs, nil, a, rowLead)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Merge(fs, nil, b, rowLead); err != nil {
			t.Fatalf("both saves are accepted while the row is filesystem: %v", err)
		}
		if err := Save(st, mergedA, secretA, fs); err != nil {
			t.Fatal(err)
		}
		if err := Finish(st); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Merge(readRow(t, st), nil, b, rowLead); !errors.Is(err, ErrFixed) {
			t.Fatalf("the second save against the stored first: %v", err)
		}
	})
}

func TestChanged(t *testing.T) {
	t.Run("nothing changed is [] and never null", func(t *testing.T) {
		// negative control: declare `var changed []string` in Changed → it marshals as null
		b, err := jsonx.Marshal(Changed(nil, Defaults(), ""))
		if err != nil || string(b) != "[]" {
			t.Fatalf("no change marshals as %s (%v)", b, err)
		}
		if got := Changed(s3Row(), s3Row().Settings, rowSecret); len(got) != 0 {
			t.Fatalf("an equal S3 row: %v", got)
		}
	})

	t.Run("a retention-only change is retention", func(t *testing.T) {
		// negative control: drop the RetentionDays comparison → []
		after := Defaults()
		after.RetentionDays = 14
		if got := Changed(nil, after, ""); !reflect.DeepEqual(got, []string{"retention"}) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("a secret rotation and a key-id rotation are credentials, not storage", func(t *testing.T) {
		// negative control: compare the S3 structs whole for storage → the key-id rotation reads [storage credentials]
		row := s3Row()
		if got := Changed(row, row.Settings, "new-secret-0002"); !reflect.DeepEqual(got, []string{"credentials"}) {
			t.Fatalf("secret rotation: %v", got)
		}
		after := s3Row().Settings
		after.Storage.S3.AccessKeyID = "AKIANEWKEY02"
		if got := Changed(row, after, rowSecret); !reflect.DeepEqual(got, []string{"credentials"}) {
			t.Fatalf("key-id rotation: %v", got)
		}
	})

	t.Run("filesystem to S3 with new chunks and retention is all four, in order", func(t *testing.T) {
		// negative control: append "credentials" before "storage" → the order differs
		after := s3Row().Settings
		after.RetentionDays = 30
		after.Chunks.Encoding = "zstd"
		want := []string{"chunks", "retention", "storage", "credentials"}
		if got := Changed(nil, after, rowSecret); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
}
