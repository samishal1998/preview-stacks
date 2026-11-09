package scaler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// OwnedWorker is one worker this scaler created. UserData never leaves scaler.json.
type OwnedWorker struct {
	Name          string            `json:"name"`
	Vars          map[string]string `json:"vars"`
	UserData      string            `json:"userData"`
	CreatedAt     int64             `json:"createdAt"`
	AppliedAt     int64             `json:"appliedAt"`
	ReadyAt       int64             `json:"readyAt"`
	Removing      bool              `json:"removing"`
	Reason        string            `json:"reason"`
	NodeID        string            `json:"nodeId"`
	StuckServices []string          `json:"stuckServices"`
	// ApplyingAt is set before every apply that includes the worker and cleared when it returns
	// (W-R1): still set on start means the apply was cut off.
	ApplyingAt int64 `json:"applyingAt"`
	// ReadySince is the first tick of the current ready-and-active run; 0 while the node is not.
	// The does-not-fit window starts here, not at the first ready.
	ReadySince int64 `json:"readySince"`
	// NotReadySince is the first tick a once-ready worker's node was absent or not ready; 0
	// while it is ready. LostAfter past it, the worker is lost (step 3).
	NotReadySince int64 `json:"notReadySince"`
	// OutOfState: removing, and an apply without it succeeded, so Tofu no longer holds it.
	OutOfState bool `json:"outOfState"`
}

// State is /state/scaler.json: the scaler's own record and the authority on what it owns.
// Private to the scaler, so fields may be added freely (W-R5).
type State struct {
	HostID         string            `json:"hostId"`
	Workers        []OwnedWorker     `json:"workers"`
	Locked         *Locked           `json:"locked"`
	LastPreset     string            `json:"lastPreset"`
	Inited         map[string]string `json:"inited"` // preset → files hash last init'd
	LastScaleUp    int64             `json:"lastScaleUp"`
	NextTryAt      int64             `json:"nextTryAt"`
	Failures       int               `json:"failures"`
	NeverJoined    int               `json:"neverJoined"`
	SettingsHash   string            `json:"settingsHash"`
	DoesNotFit     []string          `json:"doesNotFit"`
	Orphans        []string          `json:"orphans"`
	PendingDestroy string            `json:"pendingDestroy"`
	// The old-preset destroy's own backoff: its failures never hold up the new preset.
	DestroyFailures  int   `json:"destroyFailures"`
	DestroyNextTryAt int64 `json:"destroyNextTryAt"`
	// SeenAt is the last tick's wall time: a tick that takes no action shifts the clocks
	// (LastScaleUp, NextTryAt, AppliedAt) by the time since, so they do not advance.
	SeenAt int64 `json:"seenAt"`
	// MaxReported: scaler.failed {action: max} went out for the current stuck episode.
	MaxReported bool    `json:"maxReported"`
	LastError   string  `json:"lastError"`
	Events      []Event `json:"events"` // queued, posted one per report (W-R4)
}

const stateFile = "scaler.json"

// LoadState reads <dir>/scaler.json. Absent → a new state with a fresh 8-hex HostID, saved at once
// so the id is generated only once.
func LoadState(dir string) (*State, error) {
	s := &State{}
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	switch {
	case err == nil:
		if err := json.Unmarshal(b, s); err != nil {
			return nil, fmt.Errorf("%s: %w", stateFile, err)
		}
	case errors.Is(err, os.ErrNotExist):
		id := make([]byte, 4)
		if _, err := rand.Read(id); err != nil {
			return nil, err
		}
		s.HostID = hex.EncodeToString(id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := s.Save(dir); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	if s.Inited == nil {
		s.Inited = map[string]string{}
	}
	return s, nil
}

// Save writes <dir>/scaler.json at 0600 through .next + rename.
func (s *State) Save(dir string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, stateFile), b)
}

// WriteTFVars writes <dir>/workers.auto.tfvars.json at 0600 through .next + rename. Variables
// reach Tofu only this way, never on argv.
func WriteTFVars(dir string, v map[string]any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "workers.auto.tfvars.json"), b)
}

// writeAtomic never leaves path half written. A stale .next from a crash is removed first: a
// truncating write would keep its old mode.
func writeAtomic(path string, b []byte) error {
	next := path + ".next"
	if err := os.Remove(next); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(next, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(next)
		return err
	}
	return os.Rename(next, path)
}

// Lock takes <dir>/scaler.lock exclusively, so two scalers never share a state dir. The lock
// dies with the process.
func Lock(dir string) (release func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, "scaler.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another pstack scaler holds %s", dir)
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
