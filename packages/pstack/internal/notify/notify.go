// Package notify delivers domain events to the outside world.
//
// ── THE SEAM ─────────────────────────────────────────────────────────────────────────────────────
//
// A Type is `{kind, label, fields, validate, send}`. Types maps kind → type. Adding Slack, Discord
// or email later is one entry in that map: a Validate for its own config shape and a Send that
// formats its own body. No schema migration (the registration's config is opaque JSON), no change
// to the bus, no change to any route.
//
// ── NOTHING AWAITS DELIVERY ──────────────────────────────────────────────────────────────────────
//
// Dispatch is called from the bus, which is called from inside a job's finish and from request
// handlers. It starts work and returns. Every attempt carries a timeout, and MaxInFlight bounds
// concurrent sockets: this process also runs docker, and an event burst across twenty notifiers
// would otherwise exhaust its file descriptors.
//
// ── WHY THE URL IS CHECKED, AND WHAT THAT IS NOT ─────────────────────────────────────────────────
//
// The notifier URL is the one field that turns this control plane into an HTTP client aimed at an
// address of someone else's choosing — including 169.254.169.254 (cloud metadata) or
// 127.0.0.1:7878 (this very API). So loopback / link-local / private ranges are refused, and a
// redirect is a FAILURE rather than followed. This is not a privilege boundary: anyone who can
// register a notifier can already submit a spec whose hooks are `bash -c` on this host. The check
// exists so a typo aimed at an internal address fails loudly, and PSTACK_NOTIFY_ALLOW_PRIVATE=1
// escapes it for an internal collector on the same box.
package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/redact"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/webhooks"
)

// Error is a refused config or an unknown type. The API maps it to 400.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is a *Error.
func IsError(err error) bool { _, ok := err.(*Error); return ok }

// AttemptTimeout per attempt: long enough for a cold lambda, short enough that three attempts stay
// bounded.
const AttemptTimeout = 5 * time.Second

// RetryDelays: attempt 1 is immediate; these are the waits BEFORE attempts 2 and 3. Worst case ~21s.
var RetryDelays = []time.Duration{time.Second, 5 * time.Second}

// MaxInFlight is concurrent in-flight deliveries across all notifiers.
const MaxInFlight = 8

// MaxQueuedPerNotifier is how many events may wait for ONE notifier before the oldest is dropped.
// Sized for a burst, not for an outage — that is what redelivery is for.
const MaxQueuedPerNotifier = 200

// Result is one attempt's verdict.
type Result struct {
	OK     bool
	Status *int64
	Error  string
}

// Field is a form field the UI renders for this type's config.
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	Required    bool   `json:"required"`
	// Secret marks CREDENTIAL MATERIAL that must never be returned by a read path: a Slack incoming
	// webhook URL is the credential, a plain webhook's url is not. The type knows; nothing else can.
	// Genuinely absent on the wire for a plain webhook (rule 2: pointer + omitempty).
	Secret *bool `json:"secret,omitempty"`
}

// Type is one delivery module.
type Type struct {
	Kind   string
	Label  string
	Fields []Field
	// Signs: does this type use the HMAC signing secret? false for anything whose config already
	// carries its credential.
	Signs bool
	// Validate returns an *Error naming the fix. Called before anything is stored.
	Validate func(config *omap.Map) error
	// Send delivers one event. CONTRACT: never panics — return Result{OK:false, Error} instead. The
	// dispatcher recovers anyway. `data.test === true` marks a TEST delivery.
	Send func(ctx context.Context, e events.Event, config *omap.Map, secret string) Result
}

// ── URL safety ──────────────────────────────────────────────────────────────────────────────────

// IsPrivateHost: is this host one the control plane should refuse to call?
//
// A classifier, not a regexp: decide what kind of host it is first, then apply the rule for that
// kind. A DNS name is never tested against IP rules (`fcm.googleapis.com` is not `fc00::`), and an
// IP literal is never tested against name rules. hostname is as url.Hostname() gives it — bare,
// brackets already stripped.
func IsPrivateHost(hostname string) bool {
	// Trailing dot is a legal FQDN form and must not defeat a name comparison.
	host := strings.TrimSuffix(strings.ToLower(hostname), ".")
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap().WithZone("") // ::ffff:a.b.c.d and fe80::1%eth0
		if addr.Is4() {
			b := addr.As4()
			if b[0] == 0 {
				return true
			}
		}
		return addr.IsLoopback() || addr.IsUnspecified() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
	}
	// A DNS name. Only exact names, never prefixes.
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

// AssertDeliverableURL refuses anything but a public http(s) URL (unless PSTACK_NOTIFY_ALLOW_PRIVATE=1).
func AssertDeliverableURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return nil, &Error{`"` + raw + `" is not a URL`}
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, &Error{`only http and https are deliverable, got "` + scheme + `:"`}
	}
	if u.Hostname() == "" {
		return nil, &Error{`"` + raw + `" is not a URL`}
	}
	if os.Getenv("PSTACK_NOTIFY_ALLOW_PRIVATE") == "1" {
		return u, nil
	}
	if IsPrivateHost(u.Hostname()) {
		return nil, &Error{`"` + u.Hostname() + `" is a loopback, link-local or private address. The control plane would be ` +
			`making the request, so this can reach services only it can see — including cloud metadata. ` +
			`Set PSTACK_NOTIFY_ALLOW_PRIVATE=1 if that is genuinely what you want.`}
	}
	return u, nil
}

// ── the webhook type ────────────────────────────────────────────────────────────────────────────

// Envelope is the four fields a receiver gets, and the exact bytes stored for redelivery —
// SERIALISED EXACTLY ONCE. events.Event marshals to {id,event,at,data}; Replay is `json:"-"`.
func Envelope(e events.Event) []byte {
	if e.Data == nil {
		e.Data = json.RawMessage("null")
	}
	return jsonx.Must(e)
}

// Sign is `sha256=` + hex(HMAC-SHA256(secret, "<timestamp>.<body>")).
func Sign(secret string, timestamp int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(timestamp, 10) + "."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// transport never follows a redirect: a public host that 302s to the metadata endpoint is exactly
// how a registration-time address check gets defeated. RoundTrip rather than a Client with
// CheckRedirect: the Client parses the Location header BEFORE asking, and an unparsable one (a
// receiver echoing text into it) would surface as its own error instead of the 3xx it is.
// Timeouts come from the per-attempt context.
var transport http.RoundTripper = http.DefaultTransport

func post(ctx context.Context, target string, headers [][2]string, body []byte) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return Result{Error: err.Error()}
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	r, err := transport.RoundTrip(req)
	if err != nil {
		// Includes the timeout. The message may carry the URL (Go's does, Bun's did not) — the
		// caller redacts it, and for a chat webhook the URL is the credential.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Result{Error: err.Error()}
	}
	defer r.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
	status := int64(r.StatusCode)
	if status >= 300 && status < 400 {
		loc := r.Header.Get("location")
		if loc == "" {
			loc = "?"
		}
		return Result{Status: &status, Error: "redirect to " + loc + " not followed"}
	}
	ok := status >= 200 && status < 300
	res := Result{OK: ok, Status: &status}
	if !ok {
		res.Error = "HTTP " + strconv.FormatInt(status, 10)
	}
	return res
}

// WebhookType is the plain signed HTTP webhook. The URL is an address, not a credential — the HMAC
// secret proves authenticity, so the URL stays readable in the list view.
var WebhookType = &Type{
	Kind:   "webhook",
	Signs:  true,
	Label:  "HTTP webhook",
	Fields: []Field{{Key: "url", Label: "URL", Placeholder: "https://example.com/hooks/pstack", Required: true}},
	Validate: func(config *omap.Map) error {
		u, ok := config.Get("url")
		s, isStr := u.(string)
		if !ok || !isStr || strings.TrimSpace(s) == "" {
			return &Error{"a webhook needs a `url`"}
		}
		_, err := AssertDeliverableURL(s)
		return err
	},
	Send: func(ctx context.Context, e events.Event, config *omap.Map, secret string) Result {
		target := config.GetString("url")
		body := Envelope(e)
		headers := [][2]string{
			{"content-type", "application/json"},
			{"user-agent", "pstack"},
			{"x-pstack-event", e.Event},
			// Stable across all attempts AND across the notifiers of one event — the receiver's
			// dedupe key.
			{"x-pstack-delivery", e.ID},
			{"x-pstack-timestamp", strconv.FormatInt(e.At, 10)},
			{"x-pstack-signature", Sign(secret, e.At, body)},
		}
		if e.Replay {
			// A REPLAY of an event this receiver was already sent, carrying the original id.
			headers = append(headers, [2]string{"x-pstack-redelivery", "1"})
		}
		return post(ctx, target, headers, body)
	},
}

// ── chat types ──────────────────────────────────────────────────────────────────────────────────

func data(e events.Event) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(e.Data, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// tpl is `${v}` in a template literal.
func tpl(v any, present bool) string {
	if !present {
		return "undefined"
	}
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case float64:
		return jsonx.NumberString(x)
	case bool:
		return strconv.FormatBool(x)
	case []any:
		parts := make([]string, len(x))
		for i, p := range x {
			parts[i] = tpl(p, true)
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

func field(d map[string]any, k string) string { v, ok := d[k]; return tpl(v, ok) }

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	case bool:
		return x
	default:
		return true
	}
}

func strs(v any) []string {
	xs, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = tpl(x, true)
	}
	return out
}

// Summarize is one human-readable line per event, shared by every chat-shaped type, so Slack and
// Discord can never describe the same event differently. `data.test === true` is the contract flag
// from POST /:id/test: without it a test would render as a green "Deploy succeeded" nobody's deploy
// earned.
func Summarize(e events.Event) string {
	d := data(e)
	if d["test"] == true {
		return "Test delivery from pstack — the connection works. No job ran."
	}
	stack := ""
	if s, ok := d["stack"].(string); ok {
		stack = s
	} else if id, ok := d["id"]; ok && id != nil {
		stack = tpl(id, true)
	}
	took := ""
	if ms, ok := d["durationMs"].(float64); ok {
		took = " in " + jsonx.NumberString(math.Max(1, math.Floor(ms/1000+0.5))) + "s"
	}
	by := func(k string) string {
		if truthy(d[k]) {
			return " by " + tpl(d[k], true)
		}
		return ""
	}
	byStr := func(k string) string {
		if s, ok := d[k].(string); ok {
			return " by " + s
		}
		return ""
	}
	switch e.Event {
	case "deployment.created":
		return "Deployment " + field(d, "id") + " created (stack " + stack + ")."
	case "deployment.updated":
		return "Deployment " + field(d, "id") + " updated (stack " + stack + ")."
	case "deployment.deleted":
		return "Deployment " + field(d, "id") + " forgotten. Nothing was torn down."
	case "job.started":
		return actionWord(d["action"]) + " started on " + stack + "."
	case "job.succeeded":
		return actionWord(d["action"]) + " succeeded on " + stack + took + "."
	case "job.cancelled":
		return actionWord(d["action"]) + " on " + stack + " was CANCELLED" + by("cancelledBy") + took + ". Nothing was undone — verify what exists."
	case "job.failed":
		s := actionWord(d["action"]) + " FAILED on " + stack + took + "."
		if truthy(d["error"]) {
			s += " " + tpl(d["error"], true)
		}
		return s
	case "job.leaked":
		axes := ""
		if xs := strs(d["leakedAxes"]); len(xs) > 0 {
			axes = " Leaked: " + strings.Join(xs, ", ") + "."
		}
		// The event this product exists for — the wording must be impossible to skim past.
		return "Teardown LEAKED on " + stack + " — resources survived and nothing will retry." + axes
	case "spec.stored":
		verb := "stored"
		if truthy(d["replaced"]) {
			verb = "replaced"
		}
		return "Spec " + field(d, "name") + " " + verb + "."
	case "spec.deleted":
		return "Spec " + field(d, "name") + " deleted."
	case "routing.changed":
		return "Routing file " + field(d, "file") + " " + field(d, "action") + "."
	case "healthcheck.started":
		return field(d, "container") + " healthcheck " + field(d, "status") + " on " + stack + "."
	case "healthcheck.updated":
		return field(d, "container") + " healthcheck " + field(d, "previous") + " → " + field(d, "status") + " on " + stack + "."
	case "healthcheck.finished":
		verdict := "FAILED"
		if d["healthy"] == true {
			verdict = "passed"
		}
		return field(d, "container") + " healthcheck " + verdict + " on " + stack + "."
	case "healthcheck.timedout":
		waited, _ := d["waitedMs"].(float64)
		return field(d, "container") + " healthcheck never settled on " + stack + " — still starting after " + jsonx.NumberString(math.Floor(waited/1000+0.5)) + "s."
	case "container.started":
		return field(d, "container") + " was started on " + stack + by("by") + "."
	case "container.stopped":
		return field(d, "container") + " was STOPPED on " + stack + by("by") + " — it stays stopped until something starts it."
	case "container.restarted":
		return field(d, "container") + " was restarted on " + stack + by("by") + "."
	case "container.ready":
		how := " (running; no healthcheck to check)"
		if d["hasHealthcheck"] == true {
			how = " (healthcheck passed)"
		}
		return field(d, "container") + " is ready on " + stack + how + "."
	case "container.start-failed":
		s := field(d, "container") + " failed to start on " + stack
		if truthy(d["reason"]) {
			s += " — " + tpl(d["reason"], true)
		}
		return s + "."
	case "stack.ready":
		return stack + " is ready — " + field(d, "ready") + "/" + field(d, "containers") + " container(s) up" + took + "."
	case "stack.failed":
		which := ""
		if xs := strs(d["failedContainers"]); len(xs) > 0 {
			which = " Failed: " + strings.Join(xs, ", ") + "."
		}
		return stack + " did not come up" + took + "." + which
	case "stack.timedout":
		which := ""
		if xs := strs(d["pendingContainers"]); len(xs) > 0 {
			which = " Still not ready: " + strings.Join(xs, ", ") + "."
		}
		return stack + " did not become ready in time" + took + "." + which
	case "stack.slept":
		reason := ""
		if s, ok := d["reason"].(string); ok {
			reason = " (" + s + ")"
		}
		return stack + " went to sleep" + reason + ". Its data and axes stay; a request to its hostname wakes it."
	case "stack.woken":
		how := ""
		if s, ok := d["by"].(string); ok {
			how = " — " + s
		}
		return stack + " is waking up" + how + "."
	case "share.created":
		views := "details"
		if xs, ok := d["views"].([]any); ok {
			views = strings.Join(strs(xs), ", ")
		}
		return "A read-only link to " + stack + " was created" + byStr("by") + " (" + views + ")."
	default:
		if stack == "" {
			stack = "this host"
		}
		return e.Event + " on " + stack + "."
	}
}

func actionWord(action any) string {
	switch action {
	case "up":
		return "Deploy"
	case "down":
		return "Teardown"
	case "verify":
		return "Verify"
	case "sleep":
		return "Sleep"
	case "wake":
		return "Wake"
	}
	return tpl(action, action != nil)
}

// chatType: the whole difference between Slack and Discord is the URL's owner and the JSON key the
// text goes under. Signs=false and Secret=true on the URL field are the pair that makes the seam
// work: no HMAC secret is minted or shown, and every read path masks the URL.
func chatType(kind, label, placeholder, textKey string) *Type {
	return &Type{
		Kind:  kind,
		Label: label,
		Signs: false,
		Fields: []Field{{
			Key: "webhookUrl", Label: "Incoming webhook URL", Placeholder: placeholder, Required: true,
			// Anyone holding this URL can post as the app — it is the credential, not an address.
			Secret: jsonx.Bool(true),
		}},
		Validate: func(config *omap.Map) error {
			u, ok := config.Get("webhookUrl")
			s, isStr := u.(string)
			if !ok || !isStr || strings.TrimSpace(s) == "" {
				return &Error{"a " + label + " notifier needs a `webhookUrl`"}
			}
			_, err := AssertDeliverableURL(s)
			return err
		},
		Send: func(ctx context.Context, e events.Event, config *omap.Map, _ string) Result {
			body := jsonx.Must(jsonx.O(textKey, Summarize(e)))
			// Discord answers 204 on success; Slack answers 200 "ok". 2xx covers both.
			return post(ctx, config.GetString("webhookUrl"), [][2]string{{"content-type", "application/json"}, {"user-agent", "pstack"}}, body)
		},
	}
}

// SlackType's host is deliberately NOT pinned to hooks.slack.com: Mattermost and Rocket.Chat speak
// the same `{text}` payload on their own domains.
var SlackType = chatType("slack", "Slack", "https://hooks.slack.com/services/T…/B…/…", "text")

// DiscordType posts `{content}`.
var DiscordType = chatType("discord", "Discord", "https://discord.com/api/webhooks/…/…", "content")

// Types is the registry, in registration order (the order `known types:` lists them).
var Types = []*Type{WebhookType, SlackType, DiscordType}

func lookup(kind string) *Type {
	for _, t := range Types {
		if t.Kind == kind {
			return t
		}
	}
	return nil
}

// TypeOf resolves a kind or returns the unknown-type *Error.
func TypeOf(kind string) (*Type, error) {
	if t := lookup(kind); t != nil {
		return t, nil
	}
	names := make([]string, len(Types))
	for i, t := range Types {
		names[i] = t.Kind
	}
	return nil, &Error{`unknown notifier type "` + kind + `" — known types: ` + strings.Join(names, ", ")}
}

// PublicConfig is a type's config, safe to hand back: fields the type marked Secret become a mask.
func PublicConfig(kind string, config *omap.Map) *omap.Map {
	t := lookup(kind)
	if t == nil || config == nil {
		return config
	}
	secret := map[string]bool{}
	for _, f := range t.Fields {
		if f.Secret != nil && *f.Secret {
			secret[f.Key] = true
		}
	}
	if len(secret) == 0 {
		return config
	}
	out := omap.New()
	config.Each(func(k string, v any) {
		if s, ok := v.(string); ok && secret[k] {
			out.Set(k, redact.Mask(s))
		} else {
			out.Set(k, v)
		}
	})
	return out
}

// RedactForNotifier redacts a message the way the dispatcher does — every string in config, not
// just the signing secret, because for a chat webhook the URL is the credential.
func RedactForNotifier(message, secret string, config *omap.Map) string {
	extras := []string{secret}
	if config != nil {
		config.Each(func(_ string, v any) {
			if s, ok := v.(string); ok {
				extras = append(extras, s)
			}
		})
	}
	return redact.RedactText(message, extras...)
}

// ValidateConfig is for POST /api/notifiers — validates without this package knowing the store.
func ValidateConfig(kind string, config *omap.Map) error {
	t, err := TypeOf(kind)
	if err != nil {
		return err
	}
	if config == nil {
		config = omap.New()
	}
	return t.Validate(config)
}

// ── the dispatcher ──────────────────────────────────────────────────────────────────────────────

type queued struct {
	row   webhooks.NotifierRow
	event events.Event
}

// Dispatcher delivers events to subscribed notifiers.
//
// mu guards inFlight, busy, queues and order. At most ONE delivery per notifier runs at a time —
// a broken notifier must degrade itself, not everyone else — and events QUEUE per notifier,
// bounded: past the cap the OLDEST is dropped and recorded as such. The only goroutine is the
// delivery itself; Dispatch returns immediately.
type Dispatcher struct {
	Hooks *webhooks.Webhooks
	// RetryDelays and AttemptTimeout default to the package constants; tests shorten them.
	RetryDelays    []time.Duration
	AttemptTimeout time.Duration

	mu       sync.Mutex
	inFlight int
	busy     map[int64]bool
	queues   map[int64][]queued
	// order is the queues' insertion order — a JS Map iterates that way and pump walks it.
	order []int64
}

// New makes a dispatcher over a webhooks table.
func New(hooks *webhooks.Webhooks) *Dispatcher {
	return &Dispatcher{Hooks: hooks, RetryDelays: RetryDelays, AttemptTimeout: AttemptTimeout, busy: map[int64]bool{}, queues: map[int64][]queued{}}
}

// Queued is how many events are waiting for a notifier.
func (d *Dispatcher) Queued(notifierID int64) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.queues[notifierID])
}

// Dispatch is the bus listener. Returns immediately; every failure path is contained.
func (d *Dispatcher) Dispatch(e events.Event) {
	rows, err := d.Hooks.SubscribedTo(e.Event)
	if err != nil {
		return // a database that cannot be read must not break the operation that emitted
	}
	for _, row := range rows {
		d.enqueue(row, e, false)
	}
	d.pump()
}

// Redeliver sends this exact event to this notifier again, as a REPLAY — queued like any other
// delivery and marked, so the receiver can tell a replay from the original.
func (d *Dispatcher) Redeliver(row webhooks.NotifierRow, e events.Event) {
	d.enqueue(row, e, true)
	d.pump()
}

func (d *Dispatcher) enqueue(row webhooks.NotifierRow, e events.Event, replay bool) {
	if replay {
		e.Replay = true
	}
	d.mu.Lock()
	q, present := d.queues[row.ID]
	var dropped *queued
	if len(q) >= MaxQueuedPerNotifier {
		oldest := q[0]
		dropped = &oldest
		q = q[1:]
	}
	q = append(q, queued{row: row, event: e})
	d.queues[row.ID] = q
	if !present {
		d.order = append(d.order, row.ID)
	}
	d.mu.Unlock()
	if dropped != nil {
		d.drop(dropped.row, dropped.event, "dropped — "+strconv.Itoa(MaxQueuedPerNotifier)+" events already waiting for this notifier, and it is not keeping up")
	}
}

// pump starts whatever can start now. Called after every enqueue and again as each delivery
// finishes, so a freed slot is used immediately.
func (d *Dispatcher) pump() {
	d.mu.Lock()
	defer d.mu.Unlock()
	kept := d.order[:0]
	for i, id := range d.order {
		q := d.queues[id]
		if len(q) == 0 {
			delete(d.queues, id)
			continue
		}
		if d.busy[id] {
			kept = append(kept, id)
			continue
		}
		if d.inFlight >= MaxInFlight {
			kept = append(kept, d.order[i:]...) // globally saturated; the next finish re-pumps
			break
		}
		next := q[0]
		q = q[1:]
		if len(q) == 0 {
			delete(d.queues, id)
		} else {
			d.queues[id] = q
			kept = append(kept, id)
		}
		d.inFlight++
		d.busy[id] = true
		go d.run(id, next)
	}
	d.order = kept
}

func (d *Dispatcher) run(id int64, q queued) {
	func() {
		// Last resort: a notifier deleted mid-delivery makes the delivery INSERT violate its foreign
		// key; that must not take the dispatcher down.
		defer func() { _ = recover() }()
		d.deliver(q.row, q.event)
	}()
	d.mu.Lock()
	d.inFlight--
	delete(d.busy, id)
	d.mu.Unlock()
	d.pump()
}

// drop records a non-attempt. Silence here would be the exact failure this product exists to remove.
func (d *Dispatcher) drop(row webhooks.NotifierRow, e events.Event, reason string) {
	id, err := d.Hooks.StartDelivery(row.ID, e.ID, e.Event, nil)
	if err != nil {
		return // the row may have been deleted mid-dispatch
	}
	_ = d.Hooks.FinishDelivery(id, webhooks.Result{Status: "failed", Attempts: 0, Error: reason})
}

func (d *Dispatcher) deliver(row webhooks.NotifierRow, e events.Event) {
	t := lookup(row.Type)
	if t == nil {
		return
	}
	secret, err := d.Hooks.SecretOf(row.ID)
	if err != nil || secret == nil {
		return // deleted between the read and here
	}
	deliveryID, err := d.Hooks.StartDelivery(row.ID, e.ID, e.Event, Envelope(e))
	if err != nil {
		return
	}
	last := Result{Error: "not attempted"}
	attempt := 0
	for {
		attempt++
		last = d.attempt(t, e, row.Config, *secret)
		if last.OK || attempt-1 >= len(d.RetryDelays) {
			break
		}
		time.Sleep(d.RetryDelays[attempt-1])
		// DELETE returns 200 immediately; without this the endpoint the operator just unregistered
		// still receives two more signed POSTs over the following 6 seconds.
		if s, err := d.Hooks.SecretOf(row.ID); err != nil || s == nil {
			last.Error = "notifier deleted before retry"
			break
		}
	}
	status := "failed"
	if last.OK {
		status = "ok"
	}
	errText := ""
	if last.Error != "" {
		errText = RedactForNotifier(last.Error, *secret, row.Config)
	}
	_ = d.Hooks.FinishDelivery(deliveryID, webhooks.Result{Status: status, Attempts: int64(attempt), ResponseCode: last.Status, Error: errText})
	_ = d.Hooks.NoteResult(row.ID, status)
	_ = d.Hooks.Prune(row.ID)
}

// attempt is one Send under its timeout, with the contract breach (a panic) contained.
func (d *Dispatcher) attempt(t *Type, e events.Event, config *omap.Map, secret string) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			res = Result{Error: errText(p)}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), d.AttemptTimeout)
	defer cancel()
	if config == nil {
		config = omap.New()
	}
	return t.Send(ctx, e, config, secret)
}

func errText(p any) string {
	if err, ok := p.(error); ok {
		return err.Error()
	}
	return tpl(p, true)
}
