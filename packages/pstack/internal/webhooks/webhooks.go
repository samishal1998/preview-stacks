// Package webhooks is notifier registrations and their delivery log.
//
// ── THE COMPOSABILITY SEAM IS THE `type` COLUMN ──────────────────────────────────────────────────
//
// A registration is `{ type, name, events[], config{} }`. `type` selects a delivery module (see
// package notify) and OWNS the shape of `config`. Adding Slack later is a new entry in the notifier
// registry and a new `config` validator; not a migration, not a schema change, not a new table.
// `config` is stored as opaque JSON here and validated in the module that understands it.
//
// ── THE SIGNING SECRET IS STORED REVERSIBLY, DELIBERATELY ────────────────────────────────────────
//
// Sessions and tokens in package auth are SHA-256'd because they are INBOUND credentials. A signing
// secret is OUTBOUND: this process must compute `HMAC(secret, body)` on every delivery, so it needs
// the plaintext. The precedent is package registries, not auth: reversible at rest, protected by the
// 0700 directory and 0600 database file, and — the part enforced here — no function in this package
// returns it except SecretOf, and only notify calls that. List and Get project columns explicitly.
//
// ── WHY DELIVERIES ARE PERSISTED AND BOUNDED ─────────────────────────────────────────────────────
//
// jobs keeps the last 50 in memory and evicts, so a retry that re-read a job minutes later could
// find it gone. Everything a delivery needs is captured at emit time and written here; Prune caps
// the rows per notifier.
package webhooks

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// Error is a refused registration. The API maps it to 400.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is a *Error.
func IsError(err error) bool { _, ok := err.(*Error); return ok }

// MaxDeliveriesPerNotifier: enough to debug a failing endpoint, bounded on disk.
const MaxDeliveriesPerNotifier = 200

var nameRe = regexp.MustCompile(`^[\w][\w .:@/-]{0,63}$`)

// NotifierRow is what a caller may see. Note the absence of `secret` — that is the security
// property, in the type.
type NotifierRow struct {
	ID     int64     `json:"id"`
	Type   string    `json:"type"`
	Name   string    `json:"name"`
	Config *omap.Map `json:"config"`
	// Events is event names, or the single entry "*" meaning everything including future events.
	Events     []string `json:"events"`
	Enabled    bool     `json:"enabled"`
	CreatedAt  int64    `json:"createdAt"`
	LastStatus *string  `json:"lastStatus"`
	LastAt     *int64   `json:"lastAt"`
}

// DeliveryRow is one delivery as listed.
type DeliveryRow struct {
	ID           int64   `json:"id"`
	NotifierID   int64   `json:"notifierId"`
	EventID      string  `json:"eventId"`
	Event        string  `json:"event"`
	Status       string  `json:"status"`
	Attempts     int64   `json:"attempts"`
	ResponseCode *int64  `json:"responseCode"`
	Error        *string `json:"error"`
	CreatedAt    int64   `json:"createdAt"`
	UpdatedAt    int64   `json:"updatedAt"`
}

// PublicConfig masks credential-bearing config fields for display. Injected rather than imported,
// so this package still knows nothing about what a webhook is.
type PublicConfig func(kind string, c *omap.Map) *omap.Map

// Columns a caller may read. Written out rather than `SELECT *` so adding a secret-bearing column
// later cannot silently start leaking it through every existing read path.
const publicColumns = "id, type, name, config, events, enabled, created_at, last_status, last_at"

// Webhooks is the notifiers and deliveries tables. Owner: the server.
type Webhooks struct {
	Store  *store.Store
	public PublicConfig
}

// New wraps a store. publicConfig nil = identity.
func New(s *store.Store, publicConfig PublicConfig) *Webhooks {
	if publicConfig == nil {
		publicConfig = func(_ string, c *omap.Map) *omap.Map { return c }
	}
	return &Webhooks{Store: s, public: publicConfig}
}

func scanRow(sc interface{ Scan(...any) error }) (NotifierRow, error) {
	var r NotifierRow
	var config, evs string
	var enabled int
	if err := sc.Scan(&r.ID, &r.Type, &r.Name, &config, &evs, &enabled, &r.CreatedAt, &r.LastStatus, &r.LastAt); err != nil {
		return r, err
	}
	r.Enabled = enabled == 1
	r.Config = parseConfig(config)
	r.Events = []string{}
	_ = json.Unmarshal([]byte(evs), &r.Events)
	if r.Events == nil {
		r.Events = []string{}
	}
	return r, nil
}

func parseConfig(s string) *omap.Map {
	v, err := omap.Parse([]byte(s))
	if m, ok := v.(*omap.Map); ok && err == nil {
		return m
	}
	return omap.New()
}

func (w *Webhooks) raw() ([]NotifierRow, error) {
	rows, err := w.Store.DB.Query("SELECT " + publicColumns + " FROM notifiers ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifierRow{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// List is every row, with credential-bearing config fields masked.
//
// SubscribedTo deliberately does NOT go through the mask — the dispatcher needs the real values to
// make the request. Conflating the two is how a masked value ends up POSTed to a masked URL.
func (w *Webhooks) List() ([]NotifierRow, error) {
	rows, err := w.raw()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Config = w.public(rows[i].Type, rows[i].Config)
	}
	return rows, nil
}

// Get is one masked row; nil when unknown.
func (w *Webhooks) Get(id int64) (*NotifierRow, error) {
	r, err := scanRow(w.Store.DB.QueryRow("SELECT "+publicColumns+" FROM notifiers WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Config = w.public(r.Type, r.Config)
	return &r, nil
}

// SubscribedTo is every enabled notifier subscribed to this event, unmasked. The dispatcher's only
// read path. Filtered here rather than with a LIKE on the JSON: the list is tiny and a LIKE would
// match 'job.failed' inside 'job.failed_x'.
func (w *Webhooks) SubscribedTo(event string) ([]NotifierRow, error) {
	rows, err := w.raw()
	if err != nil {
		return nil, err
	}
	out := []NotifierRow{}
	for _, n := range rows {
		if !n.Enabled {
			continue
		}
		for _, e := range n.Events {
			if e == event || e == events.Wildcard {
				out = append(out, n)
				break
			}
		}
	}
	return out, nil
}

// RawConfigOf is the UNMASKED config, for delivery only — the test route and the dispatcher.
func (w *Webhooks) RawConfigOf(id int64) (*omap.Map, error) {
	var s string
	err := w.Store.DB.QueryRow("SELECT config FROM notifiers WHERE id = ?", id).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseConfig(s), nil
}

// SecretOf is the signing secret, for package notify only. Named to be conspicuous in a diff: a
// second caller is the moment to ask whether it should exist. nil when unknown.
func (w *Webhooks) SecretOf(id int64) (*string, error) {
	var s string
	err := w.Store.DB.QueryRow("SELECT secret FROM notifiers WHERE id = ?", id).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateArgs is a registration.
type CreateArgs struct {
	Type   string
	Name   string
	Config *omap.Map
	Events []string
	// ValidateConfig is supplied by the notifier registry so this package never learns what a
	// Slack config looks like.
	ValidateConfig func(kind string, c *omap.Map) error
	// Signs: whether this type signs. A type whose config already carries its credential gets no
	// secret — minting one anyway would leave a live credential nothing can use or rotate.
	Signs bool
}

// Create registers a notifier. Returns the row AND the generated signing secret (nil for a
// non-signing type) — the only time the secret leaves this package toward a caller.
func (w *Webhooks) Create(a CreateArgs) (NotifierRow, *string, error) {
	if !nameRe.MatchString(a.Name) {
		return NotifierRow{}, nil, &Error{`name must be 1–64 characters of letters, digits, space, or . : @ / _ -  (got "` + a.Name + `")`}
	}
	if len(a.Events) == 0 {
		return NotifierRow{}, nil, &Error{"subscribe to at least one event. Known events: " + strings.Join(events.Names, ", ")}
	}
	// Refused at write time, not discovered as silence months later.
	unknown := []string{}
	for _, e := range a.Events {
		if !events.IsSubscribable(e) {
			unknown = append(unknown, e)
		}
	}
	if len(unknown) > 0 {
		return NotifierRow{}, nil, &Error{"unknown event(s): " + strings.Join(unknown, ", ") + ". Known events: " +
			strings.Join(events.Names, ", ") + ` (or "` + events.Wildcard + `" for all)`}
	}
	if a.Config == nil {
		a.Config = omap.New()
	}
	if a.ValidateConfig != nil {
		if err := a.ValidateConfig(a.Type, a.Config); err != nil {
			return NotifierRow{}, nil, err
		}
	}
	secret := ""
	if a.Signs {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return NotifierRow{}, nil, err
		}
		secret = "whsec_" + hex.EncodeToString(b)
	}
	cfg, err := jsonx.Marshal(a.Config)
	if err != nil {
		return NotifierRow{}, nil, err
	}
	evs, _ := jsonx.Marshal(dedupe(a.Events))
	row, err := scanRow(w.Store.DB.QueryRow(
		"INSERT INTO notifiers (type, name, config, events, secret, created_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING "+publicColumns,
		a.Type, a.Name, string(cfg), string(evs), secret, time.Now().UnixMilli()))
	if err != nil {
		return NotifierRow{}, nil, err
	}
	if !a.Signs {
		return row, nil, nil
	}
	return row, &secret, nil
}

// SetEnabled flips the flag; false when unknown.
func (w *Webhooks) SetEnabled(id int64, enabled bool) (bool, error) {
	return changed(w.Store.DB.Exec("UPDATE notifiers SET enabled = ? WHERE id = ?", b2i(enabled), id))
}

// Remove deletes a notifier; deliveries cascade.
func (w *Webhooks) Remove(id int64) (bool, error) {
	return changed(w.Store.DB.Exec("DELETE FROM notifiers WHERE id = ?", id))
}

// ── deliveries ────────────────────────────────────────────────────────────────────────────────

// StartDelivery opens a row. Everything needed is captured NOW — the source job may be evicted
// later. payload is the whole envelope as sent (nil = none), and is what makes REDELIVERY possible.
func (w *Webhooks) StartDelivery(notifierID int64, eventID, event string, payload []byte) (int64, error) {
	now := time.Now().UnixMilli()
	var p any
	if payload != nil {
		p = string(payload)
	}
	var id int64
	err := w.Store.DB.QueryRow(
		"INSERT INTO deliveries (notifier_id, event_id, event, status, created_at, updated_at, payload) VALUES (?, ?, ?, 'pending', ?, ?, ?) RETURNING id",
		notifierID, eventID, event, now, now, p).Scan(&id)
	return id, err
}

// Delivery is one delivery with its stored payload — the read behind redelivery. Payload is nil
// for rows written before 0.25.0 and for drops that never had an envelope; the caller must refuse
// those by name rather than inventing an event to replay.
type Delivery struct {
	ID         int64
	NotifierID int64
	EventID    string
	Event      string
	Payload    *string
}

// DeliveryWithPayload is nil when unknown.
func (w *Webhooks) DeliveryWithPayload(id int64) (*Delivery, error) {
	var d Delivery
	err := w.Store.DB.QueryRow("SELECT id, notifier_id, event_id, event, payload FROM deliveries WHERE id = ?", id).
		Scan(&d.ID, &d.NotifierID, &d.EventID, &d.Event, &d.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Result is a finished delivery. Status is "ok" or "failed".
type Result struct {
	Status       string
	Attempts     int64
	ResponseCode *int64
	Error        string
}

// FinishDelivery records the outcome. Only the first 300 chars of the error's first line are kept:
// a wall of HTML from an error page belongs in nobody's database.
func (w *Webhooks) FinishDelivery(id int64, r Result) error {
	var errText any
	if r.Error != "" {
		first, _, _ := strings.Cut(r.Error, "\n")
		errText = js.Truncate(first, 300)
	}
	var code any
	if r.ResponseCode != nil {
		code = *r.ResponseCode
	}
	_, err := w.Store.DB.Exec("UPDATE deliveries SET status = ?, attempts = ?, response_code = ?, error = ?, updated_at = ? WHERE id = ?",
		r.Status, r.Attempts, code, errText, time.Now().UnixMilli(), id)
	return err
}

// NoteResult mirrors the last outcome onto the notifier, so a list view answers "is this thing
// working".
func (w *Webhooks) NoteResult(notifierID int64, status string) error {
	_, err := w.Store.DB.Exec("UPDATE notifiers SET last_status = ?, last_at = ? WHERE id = ?", status, time.Now().UnixMilli(), notifierID)
	return err
}

// Deliveries lists the newest first. limit is clamped to 1..500.
func (w *Webhooks) Deliveries(notifierID int64, limit int64) ([]DeliveryRow, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := w.Store.DB.Query(`SELECT id, notifier_id, event_id, event, status, attempts, response_code, error, created_at, updated_at
           FROM deliveries WHERE notifier_id = ? ORDER BY created_at DESC LIMIT ?`, notifierID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliveryRow{}
	for rows.Next() {
		var d DeliveryRow
		if err := rows.Scan(&d.ID, &d.NotifierID, &d.EventID, &d.Event, &d.Status, &d.Attempts, &d.ResponseCode, &d.Error, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Prune caps the log per notifier. Called after each delivery.
//
// `status != 'pending'` is not decoration: a delivery retrying against a slow endpoint lives for
// up to 21 seconds with its row already inserted; a burst of newer deliveries would push it out of
// the keep-set and DELETE it, and the later FinishDelivery would update zero rows.
func (w *Webhooks) Prune(notifierID int64) error {
	_, err := w.Store.DB.Exec(`DELETE FROM deliveries WHERE notifier_id = ? AND status != 'pending' AND id NOT IN (
           SELECT id FROM deliveries WHERE notifier_id = ? ORDER BY created_at DESC LIMIT ?
         )`, notifierID, notifierID, MaxDeliveriesPerNotifier)
	return err
}

func changed(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// dedupe is `[...new Set(xs)]`: first occurrence wins, order kept.
func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
