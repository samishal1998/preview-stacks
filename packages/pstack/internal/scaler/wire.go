// Package scaler adds and removes swarm workers through OpenTofu. It talks to pstack over HTTP
// only; these are the JSON shapes both sides speak.
package scaler

import "encoding/json"

// Settings is what an admin sets through PUT /api/scaler. Vars are strings; one starting with
// `[` or `{` reaches Tofu as JSON (P-R6).
type Settings struct {
	Preset      string            `json:"preset"`
	Vars        map[string]string `json:"vars"`
	Min         int               `json:"min"`
	Max         int               `json:"max"`
	CooldownMin int               `json:"cooldownMin"`
	GraceMin    int               `json:"graceMin"`
	// Dismissed names possible orphans the operator has dismissed (W-R18); each must be in the
	// current report's orphans.
	Dismissed []string `json:"dismissed"`
}

// Config is GET /api/scaler/config: everything the scaler needs, credential values and preset
// files included. Root only.
type Config struct {
	Settings
	Credentials map[string]string `json:"credentials"`
	Files       map[string]string `json:"files"`
}

// Worker is one owned worker as reported. Never carries user_data.
type Worker struct {
	Name      string            `json:"name"`
	Vars      map[string]string `json:"vars"`
	State     string            `json:"state"` // booting|ready|down|lost|removing
	CreatedAt int64             `json:"createdAt"`
	Removing  bool              `json:"removing"`
}

// Locked is the config fixed while the scaler owns any worker.
type Locked struct {
	Preset    string            `json:"preset"`
	FilesHash string            `json:"filesHash"`
	Shared    map[string]string `json:"shared"`
}

// Event is one event for pstack to emit. Data is marshalled by the scaler in spec field order
// and passed to the bus untouched (W-R3).
type Event struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

// Report is POST /api/scaler/report. Locked null clears the lock.
type Report struct {
	At             int64    `json:"at"`
	HostID         string   `json:"hostId"`
	Workers        []Worker `json:"workers"`
	Locked         *Locked  `json:"locked"`
	LastAction     string   `json:"lastAction"`
	LastError      string   `json:"lastError"`
	NextRetryAt    int64    `json:"nextRetryAt"`
	DoesNotFit     []string `json:"doesNotFit"`
	Orphans        []string `json:"orphans"`
	PendingDestroy string   `json:"pendingDestroy"`
	Event          *Event   `json:"event,omitempty"`
}
