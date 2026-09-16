// Package loki is Loki's settings: what an operator may change, the ranges pstack holds them to, and
// the bytes they become in control/loki/config.yaml and control/loki/s3-credentials.
//
// ── PSTACK CLAMPS, BECAUSE LOKI DOES NOT ─────────────────────────────────────────────────────────
//
//	retentionDays             1–365     retention_period AND max_query_lookback, <d×24>h
//	chunks.idlePeriodMinutes  5–60      chunk_idle_period; never above the max age
//	chunks.maxAgeMinutes      30–180    max_chunk_age; query_ingesters_within = max age + 60m
//	chunks.targetSizeKiB      512–1536  chunk_target_size; the ceiling is slice 1's value
//	chunks.encoding           snappy gzip lz4 zstd
//
// Loki range-checks none of the chunk values but the encoding. A max age at or above
// query_ingesters_within hides unflushed chunks from queries, so the window is derived, not set. The
// size ceiling is slice 1's because memory grows with it and mem_limit/GOMEMLIMIT are not settings.
// The ranges live in ONE value (bounds): GET /api/logging serves it as `limits`, Validate enforces
// it, and the two cannot drift.
//
// ── RENDERING IS LITERAL ─────────────────────────────────────────────────────────────────────────
//
// Render starts from slice 1's embedded file and walks an ordered anchor list: strings.Contains, then
// strings.Replace(n=1) (invariant 18, rule 8). An anchor is the value half of a line, so trailing
// comments survive. All ten are checked on every render — on filesystem the three S3 anchors write
// themselves back — so a template edit that moves one fails the next save by name, not the first S3
// save months later. With Defaults() every replacement writes the text it found: the default bytes
// ARE slice 1's, and a slice-1 host reconciles to nothing.
//
// Nothing is quoted or escaped. Render runs Validate's value checks before it writes a byte, so every
// string it renders passed a charset rule and every number is an int, whoever the caller is.
//
// ── S3 IS A SECOND PERIOD; CREDENTIALS NEVER TOUCH config.yaml ───────────────────────────────────
//
// Filesystem → S3 appends a schema period from the cutover and never edits one: a period whose date
// has passed cannot change without making its data unreadable. The keys go in an AWS
// shared-credentials file the SDK re-reads on every restart (AWS_SHARED_CREDENTIALS_FILE, set by
// init). `docker restart` keeps a container's environment, so a key in the env would never rotate.
// config.yaml stays 0644 and credential-free. s3-credentials is 0600 and must be Loki's: root chowns
// it, Loki's own uid writes it as is, any other euid is refused (ErrNeedsRoot). A root-owned 0600
// file would verify, go ready and fail only at the first flush after the cutover.
//
// ── ERRORS ───────────────────────────────────────────────────────────────────────────────────────
//
// *Error is a refused value: 400 with its sentence. ErrOneWay, ErrFixed and ErrNeedsRoot conflict
// with the host's state: handlers map them to 409 with errors.Is (the inspect.ErrNoDocker shape), and
// one that escapes to fail() is a 500, never a mislabelled 400.
//
// Pure: it may import pstack, store, yamlx, omap/jsonx and stdlib, nothing that reaches a server or
// docker, so its tests need neither.
package loki

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// Settings is what an operator saved. Field order is the JSON order (rule 1).
type Settings struct {
	RetentionDays int     `json:"retentionDays"`
	Chunks        Chunks  `json:"chunks"`
	Storage       Storage `json:"storage"`
}

// Chunks is the ingester's chunk shape.
type Chunks struct {
	IdlePeriodMinutes int    `json:"idlePeriodMinutes"`
	MaxAgeMinutes     int    `json:"maxAgeMinutes"`
	TargetSizeKiB     int    `json:"targetSizeKiB"`
	Encoding          string `json:"encoding"`
}

// Storage is filesystem, or filesystem then S3 from a cutover.
type Storage struct {
	Type string `json:"type"` // StorageFilesystem | StorageS3
	S3   *S3    `json:"s3"`   // null on filesystem — a pointer without omitempty (rule 2)
}

// S3 is an S3-compatible store. The secret is not here: it is its own column and has no read path.
type S3 struct {
	Endpoint    string `json:"endpoint"` // http(s)://host[:port]
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	PathStyle   bool   `json:"pathStyle"`
	AccessKeyID string `json:"accessKeyId"`
	Cutover     string `json:"cutover"` // YYYY-MM-DD UTC: the S3 period's `from`
}

// The storage types.
const (
	StorageFilesystem = "filesystem"
	StorageS3         = "s3"
)

// The files, in <LokiDir>. A write goes to <name>.next and is renamed over <name>.
const (
	ConfigFile      = "config.yaml"
	CredentialsFile = "s3-credentials"
	NextSuffix      = ".next"
)

// Defaults is slice 1's config: what an empty loki_config table means.
func Defaults() Settings {
	return Settings{
		RetentionDays: 7,
		Chunks:        Chunks{IdlePeriodMinutes: 30, MaxAgeMinutes: 120, TargetSizeKiB: 1536, Encoding: "snappy"},
		Storage:       Storage{Type: StorageFilesystem},
	}
}

// Range is an inclusive bound.
type Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Limits is GET /api/logging's `limits`, so the UI never hard-codes a range or a date.
type Limits struct {
	RetentionDays     Range    `json:"retentionDays"`
	IdlePeriodMinutes Range    `json:"idlePeriodMinutes"`
	MaxAgeMinutes     Range    `json:"maxAgeMinutes"`
	TargetSizeKiB     Range    `json:"targetSizeKiB"`
	Encodings         []string `json:"encodings"`
	EarliestCutover   string   `json:"earliestCutover"`
}

// bounds is every range Validate enforces. Never handed out: LimitsAt copies it.
var bounds = Limits{
	RetentionDays:     Range{1, 365},
	IdlePeriodMinutes: Range{5, 60},
	MaxAgeMinutes:     Range{30, 180},
	TargetSizeKiB:     Range{512, 1536},
	Encodings:         []string{"snappy", "gzip", "lz4", "zstd"},
}

// LimitsAt is the limits at t. Encodings is a fresh non-nil slice (rule 3), so a caller cannot edit
// what Validate reads.
func LimitsAt(t time.Time, lead time.Duration) Limits {
	l := bounds
	l.Encodings = slices.Clone(bounds.Encodings)
	l.EarliestCutover = EarliestCutover(t, lead)
	return l
}

// EarliestCutover is the first UTC date whose 00:00 is strictly after t + 2×lead + 10m: the swap, a
// failed ready wait and a whole rollback all finish before the S3 period starts. Truncating to 24h
// lands on a UTC midnight at or before the window's end, so the next one is strictly after it.
func EarliestCutover(t time.Time, lead time.Duration) string {
	return t.Add(2*lead+10*time.Minute).UTC().Truncate(24*time.Hour).AddDate(0, 0, 1).Format(time.DateOnly)
}

// Lead is one restart plus a ready wait, with slack.
func Lead(readyTimeout time.Duration) time.Duration { return readyTimeout + 10*time.Minute }

// Error is a refused value. The API maps it to 400.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is, or wraps, a *Error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// The conflicts with the host's state. Not *Error: handlers answer 409 with errors.Is.
var (
	ErrOneWay    = errors.New("S3 is one-way on this host")
	ErrFixed     = errors.New("S3 storage is fixed once saved — only accessKeyId and secretAccessKey change")
	ErrNeedsRoot = errors.New("the S3 credentials file needs root")
)

// Seams: tests pin the clock and the process identity. Never t.Parallel in a test that sets one.
var (
	now     = time.Now
	chown   = os.Chown
	geteuid = os.Geteuid
)

var (
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	regionRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

// Validate is every rule a save must pass. addingS3 is true on the save that turns S3 on: only that
// save's cutover is held to EarliestCutover; a key rotation after the cutover is not.
func Validate(s Settings, secret string, addingS3 bool, lead time.Duration) error {
	if err := check(s); err != nil {
		return err
	}
	if s.Storage.Type != StorageS3 {
		return nil
	}
	if !iniValue(secret, 8) {
		return &Error{"secretAccessKey must be 8–256 printable ASCII characters, no space, # or ;"}
	}
	if earliest := EarliestCutover(now(), lead); addingS3 && s.Storage.S3.Cutover < earliest {
		return &Error{"cutover must be " + earliest + " or later"} // YYYY-MM-DD compares as a string
	}
	return nil
}

// check is Validate without the secret and the cutover floor: the values Render writes. Storage field
// names are the storage PUT body's, which is flat.
func check(s Settings) error {
	for _, f := range []struct {
		name string
		v    int
		r    Range
	}{
		{"retentionDays", s.RetentionDays, bounds.RetentionDays},
		{"chunks.idlePeriodMinutes", s.Chunks.IdlePeriodMinutes, bounds.IdlePeriodMinutes},
		{"chunks.maxAgeMinutes", s.Chunks.MaxAgeMinutes, bounds.MaxAgeMinutes},
		{"chunks.targetSizeKiB", s.Chunks.TargetSizeKiB, bounds.TargetSizeKiB},
	} {
		if f.v < f.r.Min || f.v > f.r.Max {
			return &Error{fmt.Sprintf("%s must be %d–%d", f.name, f.r.Min, f.r.Max)}
		}
	}
	if s.Chunks.IdlePeriodMinutes > s.Chunks.MaxAgeMinutes {
		return &Error{"chunks.idlePeriodMinutes must not exceed chunks.maxAgeMinutes"}
	}
	if !slices.Contains(bounds.Encodings, s.Chunks.Encoding) {
		return &Error{"chunks.encoding must be one of " + strings.Join(bounds.Encodings, ", ")}
	}
	switch s.Storage.Type {
	case StorageFilesystem:
		if s.Storage.S3 != nil {
			return &Error{"type filesystem takes no s3 fields"}
		}
		return nil
	case StorageS3:
		if s.Storage.S3 == nil {
			return &Error{"type s3 needs its s3 fields"}
		}
	default:
		return &Error{"type must be filesystem or s3"}
	}
	c := s.Storage.S3
	if _, _, ok := endpoint(c.Endpoint); !ok {
		return &Error{"endpoint must be http(s)://host[:port]"}
	}
	if !regionRe.MatchString(c.Region) {
		return &Error{"region must be 1–32 characters of a-z, 0-9 and -"}
	}
	if !bucketRe.MatchString(c.Bucket) {
		return &Error{"bucket must be 3–63 characters of a-z, 0-9, - and ., starting and ending with a letter or digit"}
	}
	// A dotted bucket in virtual-hosted form is a host name its TLS certificate does not cover.
	if strings.Contains(c.Bucket, ".") && !c.PathStyle {
		return &Error{"bucket may contain . only with pathStyle"}
	}
	if !iniValue(c.AccessKeyID, 1) {
		return &Error{"accessKeyId must be 1–256 printable ASCII characters, no space, # or ;"}
	}
	if _, err := time.Parse(time.DateOnly, c.Cutover); err != nil {
		return &Error{"cutover must be YYYY-MM-DD"}
	}
	return nil
}

// endpoint is the host[:port] Loki takes and whether the scheme is http. The host is DNS or IPv4
// characters only: it is written into YAML unquoted, where `[` opens a flow sequence and a trailing
// `:` a mapping, so a bracketed IPv6 literal is refused.
func endpoint(raw string) (host string, insecure, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		strings.ContainsAny(raw, "?#") || (u.Path != "" && u.Path != "/") ||
		!hostRe.MatchString(u.Hostname()) || strings.HasSuffix(u.Host, ":") {
		return "", false, false
	}
	return u.Host, u.Scheme == "http", true
}

// iniValue is the credentials file's value syntax: min–256 bytes of printable ASCII without space,
// # or ; (INI comment starts). The charset is ASCII, so bytes and characters are the same count.
func iniValue(v string, min int) bool {
	if len(v) < min || len(v) > 256 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x21 || c > 0x7e || c == '#' || c == ';' {
			return false
		}
	}
	return true
}

// Credentials is s3-credentials. Both values passed iniValue, so nothing needs escaping.
func Credentials(keyID, secret string) string {
	return "[default]\naws_access_key_id = " + keyID + "\naws_secret_access_key = " + secret + "\n"
}

// CredentialsOwner is nil when this process can give Loki (lokiUID) a 0600 file it can read: as root,
// by chowning, or as Loki's own uid.
func CredentialsOwner(lokiUID int) error {
	if e := geteuid(); e == 0 || e == lokiUID {
		return nil
	}
	return ErrNeedsRoot
}

// WriteFile writes body at exactly mode. A 0600 file is refused unless CredentialsOwner passes, and as
// root it is chowned to lokiUID.
//
// Created at mode, not 0o666-then-chmod: the secret is never world-readable, even for an instant. A
// leftover (a .next from a killed apply, or a symlink) is removed first: a truncating write keeps an
// old file's mode and follows a link. The Chmod still runs, because umask may strip bits a 0644
// config.yaml needs for Loki's uid to read it.
func WriteFile(path, body string, mode os.FileMode, lokiUID int) error {
	if mode == 0o600 {
		if err := CredentialsOwner(lokiUID); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if mode == 0o600 && geteuid() == 0 && lokiUID != 0 {
		return chown(path, lokiUID, lokiUID)
	}
	return nil
}

// Writable is true when dir holds config.yaml and this process may write beside it. A mount can be
// present but `:ro`, which only a write shows (routing.RoutingStore.Writable, copied: loki does not
// import routing).
func Writable(dir string) bool {
	if st, err := os.Stat(filepath.Join(dir, ConfigFile)); err != nil || !st.Mode().IsRegular() {
		return false
	}
	probe := filepath.Join(dir, fmt.Sprintf(".pstack-write-probe-%d", os.Getpid()))
	if err := os.WriteFile(probe, nil, 0o666); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// lokiMount is where the pstack container sees control/loki: the same path as Loki's own mount, so a
// file name means the same thing in both containers.
const lokiMount = "/etc/loki"

// Dir is control/loki for THIS process (routing.DynamicDir's resolution): PSTACK_LOKI_DIR by presence
// (rule 11), then the in-container mount, then the host-side path under the data dir.
func Dir(dataDir string) string {
	if d, ok := os.LookupEnv("PSTACK_LOKI_DIR"); ok {
		return d
	}
	if st, err := os.Stat(lokiMount); err == nil && st.IsDir() {
		return lokiMount
	}
	return filepath.Join(dataDir, "control", "loki")
}

// ── THE ROW ──────────────────────────────────────────────────────────────────────────────────────
//
// loki_config (migration 9) is what an operator SAVED, never what Loki runs: every schema-period
// decision reads config.yaml. previous_* is one apply's write-ahead undo record, written in the same
// statement as the save. NULL: no apply in flight. '': the table was empty before it.
//
// Everything here uses st.DB, so none of it may run inside store.Tx (one connection, Go rule 16).

// Row is the stored settings. Previous is set only while an apply is in flight over an earlier row;
// InFlight with a nil Previous means the table was empty before, so undoing renders Defaults().
// Previous.UpdatedAt is 0: no column keeps it.
type Row struct {
	Settings  Settings
	Secret    string
	UpdatedAt int64
	InFlight  bool
	Previous  *Row
}

// Read is the stored row, or nil when the table is empty (slice 1's config exactly).
func Read(st *store.Store) (*Row, error) {
	var config string
	var previous, previousSecret sql.NullString
	r := &Row{}
	err := st.DB.QueryRow("SELECT config, secret, previous_config, previous_secret, updated_at FROM loki_config WHERE id = 1").
		Scan(&config, &r.Secret, &previous, &previousSecret, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(config), &r.Settings); err != nil {
		return nil, err
	}
	r.InFlight = previous.Valid
	if previous.Valid && previous.String != "" {
		r.Previous = &Row{Secret: previousSecret.String}
		if err := json.Unmarshal([]byte(previous.String), &r.Previous.Settings); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Save stores a save and its undo record in ONE statement, so no crash leaves one without the
// other. previous is the row the apply read (nil: the table was empty).
func Save(st *store.Store, s Settings, secret string, previous *Row) error {
	config, err := jsonx.Marshal(s)
	if err != nil {
		return err
	}
	previousConfig, previousSecret := "", ""
	if previous != nil {
		b, err := jsonx.Marshal(previous.Settings)
		if err != nil {
			return err
		}
		previousConfig, previousSecret = string(b), previous.Secret
	}
	_, err = st.DB.Exec(
		"INSERT INTO loki_config (id, config, secret, previous_config, previous_secret, updated_at) VALUES (1, ?, ?, ?, ?, ?) "+
			"ON CONFLICT(id) DO UPDATE SET config = excluded.config, secret = excluded.secret, "+
			"previous_config = excluded.previous_config, previous_secret = excluded.previous_secret, updated_at = excluded.updated_at",
		string(config), secret, previousConfig, previousSecret, now().UnixMilli())
	return err
}

// Finish ends an apply: the save stays, the undo record goes.
func Finish(st *store.Store) error {
	_, err := st.DB.Exec("UPDATE loki_config SET previous_config = NULL, previous_secret = NULL WHERE id = 1")
	return err
}

// Revert undoes an apply: the row becomes previous, or goes when previous is empty. The two WHEREs
// are disjoint and NULL matches neither, so the pair needs no transaction and leaves a row with no
// apply in flight untouched.
func Revert(st *store.Store) error {
	if _, err := st.DB.Exec("DELETE FROM loki_config WHERE previous_config = ''"); err != nil {
		return err
	}
	_, err := st.DB.Exec("UPDATE loki_config SET config = previous_config, secret = previous_secret, " +
		"previous_config = NULL, previous_secret = NULL WHERE previous_config <> ''")
	return err
}

// ChunksPatch is PUT /api/logging's body: the whole chunks-and-retention section.
type ChunksPatch struct {
	RetentionDays int
	Chunks        Chunks
}

// StoragePatch is PUT /api/logging/storage's body. KeepSecret is an empty or masked
// secretAccessKey, resolved against the row Merge is given, never the one the request saw.
type StoragePatch struct {
	Storage    Storage
	Secret     string
	KeepSecret bool
}

// Merge lays the patches over the row (Defaults() when there is none) and resolves KeepSecret.
// The storage rules run against THIS row, so a second save that queued behind a first is refused
// once the first is stored. ErrOneWay and ErrFixed (409) come before *Error (400), and Validate
// runs last. addingS3 is filesystem → s3 only, so a rotation on a host past its cutover passes.
func Merge(row *Row, c *ChunksPatch, sp *StoragePatch, lead time.Duration) (Settings, string, error) {
	base, secret := Defaults(), ""
	if row != nil {
		base, secret = row.Settings, row.Secret
	}
	merged := base
	if c != nil {
		merged.RetentionDays, merged.Chunks = c.RetentionDays, c.Chunks
	}
	if sp != nil {
		switch {
		case base.Storage.Type == StorageS3 && sp.Storage.Type == StorageFilesystem:
			return Settings{}, "", ErrOneWay
		case base.Storage.Type == StorageS3 && sp.Storage.Type == StorageS3 && fixed(base.Storage) != fixed(sp.Storage):
			return Settings{}, "", ErrFixed
		case base.Storage.Type == StorageFilesystem && sp.Storage.Type == StorageFilesystem:
			// Filesystem has no fields and no secret: nothing to change.
		default:
			merged.Storage = sp.Storage
			switch {
			case !sp.KeepSecret:
				secret = sp.Secret
			case secret == "" || keyID(base.Storage) != keyID(sp.Storage):
				// A new key id with the old secret is never right.
				return Settings{}, "", &Error{Msg: "secretAccessKey is required"}
			}
		}
	}
	addingS3 := base.Storage.Type == StorageFilesystem && merged.Storage.Type == StorageS3
	if err := Validate(merged, secret, addingS3, lead); err != nil {
		return Settings{}, "", err
	}
	return merged, secret, nil
}

// Changed is logging.changed's `changed`, in this order: chunks, retention, storage (the type or
// any S3 field but the key id), credentials (the key id or the secret). Never nil. Empty means
// after equals the row, which is how the apply and the PUT spot a no-op.
func Changed(before *Row, after Settings, afterSecret string) []string {
	b, secret := Defaults(), ""
	if before != nil {
		b, secret = before.Settings, before.Secret
	}
	changed := []string{}
	if b.Chunks != after.Chunks {
		changed = append(changed, "chunks")
	}
	if b.RetentionDays != after.RetentionDays {
		changed = append(changed, "retention")
	}
	if b.Storage.Type != after.Storage.Type || fixed(b.Storage) != fixed(after.Storage) {
		changed = append(changed, "storage")
	}
	if keyID(b.Storage) != keyID(after.Storage) || secret != afterSecret {
		changed = append(changed, "credentials")
	}
	return changed
}

// fixed is what an S3 save makes permanent: every S3 field but the key id.
func fixed(st Storage) S3 {
	if st.S3 == nil {
		return S3{}
	}
	f := *st.S3
	f.AccessKeyID = ""
	return f
}

func keyID(st Storage) string {
	if st.S3 == nil {
		return ""
	}
	return st.S3.AccessKeyID
}
