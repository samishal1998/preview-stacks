package settings

import (
	"strconv"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

func open(t *testing.T, envMaxJobs int) *Settings {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, envMaxJobs)
}

// negative control: in MaxJobs, check envMaxJobs before the stored row → "the stored value must beat
// the environment" fires. An operator who sets the cap in the UI would be silently overridden by the
// container's environment on every restart, which is the whole point of the precedence.
func TestMaxJobsPrecedence(t *testing.T) {
	s := open(t, 2)
	if got := s.MaxJobs(); got != 2 {
		t.Fatalf("with no row, PSTACK_MAX_JOBS is the default: got %d, want 2", got)
	}
	if err := s.Set(KeyMaxJobs, "8"); err != nil {
		t.Fatal(err)
	}
	if got := s.MaxJobs(); got != 8 {
		t.Fatalf("the stored value must beat the environment: got %d, want 8", got)
	}

	if got := open(t, 0).MaxJobs(); got != jobs.DefaultMaxRunning {
		t.Fatalf("no row and no environment is the built-in: got %d, want %d", got, jobs.DefaultMaxRunning)
	}

	// A row an operator hand-edited over SSH. Resolving to 0 would stall every job on the host, so
	// nonsense falls to the next source down rather than being believed.
	if _, err := s.Store.DB.Exec("UPDATE settings SET value = 'nonsense' WHERE key = ?", KeyMaxJobs); err != nil {
		t.Fatal(err)
	}
	if got := s.MaxJobs(); got != 2 {
		t.Fatalf("a garbage row must fall back to the environment: got %d, want 2", got)
	}
}

// negative control: drop the auth.ValidRole check from DefaultRole and return the stored value → the
// hand-edited 'superuser' row comes back and the least-privilege assertion fires.
func TestDefaultRoleNeverResolvesUpward(t *testing.T) {
	s := open(t, 0)
	if got := s.DefaultRole(); got != string(auth.Viewer) {
		t.Fatalf("unset must be viewer, got %q", got)
	}
	if err := s.Set(KeyDefaultRole, string(auth.Maintainer)); err != nil {
		t.Fatal(err)
	}
	if got := s.DefaultRole(); got != string(auth.Maintainer) {
		t.Fatalf("the stored role must be honoured, got %q", got)
	}
	// Garbage resolves DOWN, never to admin and never to something unrankable: an account minted
	// from this value is what an SSO provider with an empty defaultRole gets.
	for _, bad := range []string{"superuser", "ADMIN", ""} {
		if _, err := s.Store.DB.Exec("UPDATE settings SET value = ? WHERE key = ?", bad, KeyDefaultRole); err != nil {
			t.Fatal(err)
		}
		got := s.DefaultRole()
		if got != string(auth.Viewer) {
			t.Fatalf("a stored %q resolved to %q; it must be %q", bad, got, auth.Viewer)
		}
		if !auth.ValidRole(got) || got == string(auth.Admin) {
			t.Fatalf("a stored %q resolved to %q — omission must never yield admin or an unrankable role", bad, got)
		}
	}
}

// negative control: drop the `default:` arm from Set's switch → the unknown key is written and
// "unknown keys must not reach the table" fires.
func TestSetValidatesKeyAndValue(t *testing.T) {
	s := open(t, 0)

	for _, bad := range []string{"0", "-1", "1.5", "abc", "", " 8", "1e3", "9999999999999999999999"} {
		if err := s.Set(KeyMaxJobs, bad); err == nil || !IsError(err) {
			t.Fatalf("max_jobs = %q was accepted (%v)", bad, err)
		}
	}
	// Stored canonically, and a second write updates rather than duplicating.
	if err := s.Set(KeyMaxJobs, "008"); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := s.Store.DB.QueryRow("SELECT value FROM settings WHERE key = ?", KeyMaxJobs).Scan(&v); err != nil || v != "8" {
		t.Fatalf(`"008" was stored as %q (%v), want "8"`, v, err)
	}
	if err := s.Set(KeyMaxJobs, "9"); err != nil {
		t.Fatal(err)
	}
	var rows int
	s.Store.DB.QueryRow("SELECT COUNT(*) FROM settings WHERE key = ?", KeyMaxJobs).Scan(&rows)
	if s.MaxJobs() != 9 || rows != 1 {
		t.Fatalf("a second write left %d rows and MaxJobs %d", rows, s.MaxJobs())
	}

	for _, role := range []string{"viewer", "developer", "maintainer", "admin"} {
		if err := s.Set(KeyDefaultRole, role); err != nil {
			t.Fatalf("default_role = %q refused: %v", role, err)
		}
	}
	for _, bad := range []string{"superuser", "ADMIN", "", "root", "share"} {
		if err := s.Set(KeyDefaultRole, bad); err == nil || !IsError(err) {
			t.Fatalf("default_role = %q was accepted (%v)", bad, err)
		}
	}

	// An unknown key is refused AND writes nothing: this table is a closed set, not a KV store.
	if err := s.Set("shell_command", "rm -rf /"); err == nil || !IsError(err) {
		t.Fatalf("an unknown key was accepted (%v)", err)
	}
	var strays int
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM settings WHERE key NOT IN (?, ?)", KeyMaxJobs, KeyDefaultRole).Scan(&strays); err != nil {
		t.Fatal(err)
	}
	if strays != 0 {
		t.Fatalf("unknown keys must not reach the table: %d stored", strays)
	}
}

// negative control: return 80 from PressureDiskPct → "pressure_disk_pct = 80, want 90" fires.
func TestPressureSettingsDefaults(t *testing.T) {
	s := open(t, 0)
	if got := s.PressureDiskPct(); got != 90 {
		t.Fatalf("pressure_disk_pct = %d, want 90", got)
	}
	if got := s.PressureMemPct(); got != 10 {
		t.Fatalf("pressure_mem_pct = %d, want 10", got)
	}
	if got := s.PressureCPULoad(); got != 2 {
		t.Fatalf("pressure_cpu_load = %v, want 2", got)
	}
	if got := s.PressureSleepIdleMin(); got != 30 {
		t.Fatalf("pressure_sleep_idle_min = %d, want 30", got)
	}
	if got := s.ImagePruneAfterH(); got != 168 {
		t.Fatalf("image_prune_after_h = %d, want 168", got)
	}
	// A row hand-edited to nonsense is the default, never 0 (which would turn the guard off).
	if _, err := s.Store.DB.Exec("INSERT INTO settings (key, value, updated_at) VALUES (?, 'lots', 0)", KeyPressureDiskPct); err != nil {
		t.Fatal(err)
	}
	if got := s.PressureDiskPct(); got != 90 {
		t.Fatalf("a garbage row: pressure_disk_pct = %d, want 90", got)
	}
}

// negative control: drop the `n <= max` bound from wholeIn → "pressure_disk_pct = \"101\"
// accepted" fires; drop the IsInf check from load → "pressure_cpu_load = \"Inf\" accepted" fires.
func TestPressureSettingsSetAndRefuse(t *testing.T) {
	s := open(t, 0)
	cases := []struct {
		key, value string
		ok         bool
		read       func() float64
		want       float64
	}{
		{KeyPressureDiskPct, "0", true, func() float64 { return float64(s.PressureDiskPct()) }, 0},
		{KeyPressureDiskPct, "100", true, func() float64 { return float64(s.PressureDiskPct()) }, 100},
		{KeyPressureDiskPct, "101", false, nil, 0},
		{KeyPressureDiskPct, "9.5", false, nil, 0},
		{KeyPressureDiskPct, "-1", false, nil, 0},
		{KeyPressureMemPct, "15", true, func() float64 { return float64(s.PressureMemPct()) }, 15},
		{KeyPressureMemPct, "101", false, nil, 0},
		{KeyPressureCPULoad, "2.5", true, s.PressureCPULoad, 2.5},
		{KeyPressureCPULoad, "0", true, s.PressureCPULoad, 0},
		{KeyPressureCPULoad, "-1", false, nil, 0},
		{KeyPressureCPULoad, "Inf", false, nil, 0},
		{KeyPressureCPULoad, "NaN", false, nil, 0},
		{KeyPressureCPULoad, "", false, nil, 0},
		{KeyPressureSleepIdleMin, "0", true, func() float64 { return float64(s.PressureSleepIdleMin()) }, 0},
		{KeyPressureSleepIdleMin, "-5", false, nil, 0},
		{KeyPressureSleepIdleMin, "1.5", false, nil, 0},
		{KeyImagePruneAfterH, "0", true, func() float64 { return float64(s.ImagePruneAfterH()) }, 0},
		{KeyImagePruneAfterH, "abc", false, nil, 0},
	}
	for _, c := range cases {
		err := s.Set(c.key, c.value)
		if !c.ok {
			if err == nil || !IsError(err) || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("%s = %q accepted, or refused without naming the key (%v)", c.key, c.value, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s = %q refused: %v", c.key, c.value, err)
		}
		if got := c.read(); got != c.want {
			t.Fatalf("%s = %q reads %v, want %v", c.key, c.value, got, c.want)
		}
	}
	// Stored canonically: 2.0 is "2".
	if err := s.Set(KeyPressureCPULoad, "2.0"); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := s.Store.DB.QueryRow("SELECT value FROM settings WHERE key = ?", KeyPressureCPULoad).Scan(&v); err != nil || v != "2" {
		t.Fatalf(`"2.0" was stored as %q (%v), want "2"`, v, err)
	}
}

// The two bounds keep minutes × 60 000 in an int64 and `until=<N>h` a duration docker takes.
// negative control: bound both keys by math.MaxInt in Set → "pressure_sleep_idle_min = 525601: <nil>"
// fires.
// negative control: bound both keys by math.MaxInt in Usable → the hand-edited row reads 525601.
func TestPressureSettingsUpperBounds(t *testing.T) {
	s := open(t, 0)
	for _, c := range []struct {
		key      string
		max, def int
		read     func() int
	}{
		{KeyPressureSleepIdleMin, 525600, 30, s.PressureSleepIdleMin},
		{KeyImagePruneAfterH, 87600, 168, s.ImagePruneAfterH},
	} {
		top, over := strconv.Itoa(c.max), strconv.Itoa(c.max+1)
		if err := s.Set(c.key, top); err != nil || c.read() != c.max {
			t.Fatalf("%s = %s: %v, reads %d", c.key, top, err, c.read())
		}
		if err := s.Set(c.key, over); err == nil || err.Error() != c.key+" must be a whole number from 0 to "+top {
			t.Fatalf("%s = %s: %v", c.key, over, err)
		}
		if _, err := s.Store.DB.Exec("UPDATE settings SET value = ? WHERE key = ?", over, c.key); err != nil {
			t.Fatal(err)
		}
		if got := c.read(); got != c.def {
			t.Fatalf("hand-edited %s = %s reads %d, want the default %d", c.key, over, got, c.def)
		}
	}
}
