package loki

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
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
