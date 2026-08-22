// Package events is the domain event bus.
//
// One narrow job: something happened, tell whoever registered interest, and NEVER let a listener's
// failure reach the thing that happened. A webhook endpoint that is down, slow, or throwing must not
// fail a deploy — so Emit is synchronous-dispatch/asynchronous-delivery: it hands each listener the
// event inline and returns, and every listener call is individually recovered. Delivery itself (the
// network) is the listener's goroutine, never the bus's.
//
// WHY THIS EXISTS SEPARATELY from the job registry's subscriber set: that one carries LOG LINES for
// one job to one SSE stream — a transport detail with a per-job lifetime. This carries DOMAIN events
// for the whole process with a process lifetime, and its consumers (webhooks now; Slack, Discord,
// email later) care about "a teardown leaked", not about individual log lines.
//
// ── THE EVENT NAMES ARE A PUBLIC CONTRACT ────────────────────────────────────────────────────────
//
// They go into stored webhook registrations, so renaming one silently stops deliveries for everyone
// who subscribed to it. Add; do not rename. Names is the enumeration a UI offers and the API
// validates against — an unknown name in a registration is refused at write time rather than
// discovered as silence months later.
package events

import (
	"encoding/json"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
)

// Names is the TS EVENTS list, in its order. Add-only (invariant 14).
var Names = []string{
	"deployment.created",
	"deployment.updated",
	"deployment.deleted",
	"job.started",
	"job.succeeded",
	"job.failed",
	// A person stopped it part-way. Distinct from `job.failed` for the same reason `job.leaked` is:
	// a cancelled run did not try and lose, it was interrupted, and whatever it had already created
	// or destroyed is still that way with nobody watching.
	"job.cancelled",
	// The one this product exists for: teardown ran and something survived it. Distinct from
	// `job.failed` — a failed job did not do what it was told, a leaked one left a resource behind
	// that nothing else will clean up.
	"job.leaked",
	"spec.stored",
	"spec.deleted",
	"routing.changed",
	// Readiness convergence, observed by the watcher that starts after every successful deploy (see
	// the readiness package). `compose up -d` returning is not "the app works": containers still
	// have to pass their healthchecks, and a crash-on-boot exits *after* the deploy job already
	// reported success. These events carry that second half.
	//
	// The `healthcheck.*` family narrates one container's health probe: `started` when a probe is
	// first observed, `updated` on every status change, `finished` when it settles (healthy or
	// unhealthy), `timedout` when the watcher gave up while it was still starting. `container.*` is
	// the per-container verdict; `stack.*` the aggregate one — exactly one of `stack.ready`,
	// `stack.failed`, `stack.timedout` ends each watch.
	"healthcheck.started",
	"healthcheck.updated",
	"healthcheck.finished",
	"healthcheck.timedout",
	"container.ready",
	"container.start-failed",
	// A person acted on ONE container through the API (`POST …/containers/:name/(start|stop|restart)`).
	// Separate from the readiness family above, which observes what docker did on its own: these say
	// somebody chose it, and carry `by`.
	"container.started",
	"container.stopped",
	"container.restarted",
	"stack.ready",
	"stack.failed",
	"stack.timedout",
	// The scheduler (0.26.0). `stack.slept`: the compose project was taken down while its axes stayed —
	// by the idle/after policy in the spec, or by hand. `stack.woken`: something brought it back; `by` is
	// `request:<hostname>` when the catch-all router saw traffic for it, or the actor who asked.
	"stack.slept",
	"stack.woken",
	// A read-only link to one deployment was minted. Carries WHAT was granted, never the token.
	"share.created",
}

// IsEventName is `typeof v === 'string' && EVENTS.includes(v)` — v is the decoded JSON value a
// registration request carried, so it takes any.
func IsEventName(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	for _, n := range Names {
		if n == s {
			return true
		}
	}
	return false
}

// Wildcard subscribes to everything, including events added in later versions.
//
// One line here spares every operator from re-registering each time the enumeration grows — which is
// the failure mode of a strict enum: the feature works, nobody is notified, and nobody knows why.
const Wildcard = "*"

// IsSubscribable is IsEventName or the wildcard.
func IsSubscribable(v any) bool { return v == Wildcard || IsEventName(v) }

// Event is the envelope. Its JSON is EXACTLY the four fields `{id,event,at,data}` — receivers verify
// a signature over those bytes (invariant 14), and notify serialises it once via jsonx for both the
// signature and the body.
type Event struct {
	// ID is unique per emission. A receiver dedupes on this across retries.
	ID    string `json:"id"`
	Event string `json:"event"`
	// At is epoch ms. Signed along with the body, so a receiver can reject a stale replay.
	At int64 `json:"at"`
	// Data is the event-specific detail, marshalled ONCE at Emit (rule 1). Deliberately loose and
	// shaped at each emit site: a schema per event would be ceremony for a payload whose only
	// consumer is JSON on the wire.
	//
	// NOTHING SECRET GOES IN. Job outcomes carry captured credentials by design (`outcome.outputs` is
	// the inter-axis env channel), so emit sites send identity and status — ids, stack, state, counts —
	// and never the outcome object. See `docs/secret-exposure.md` for why that channel is the way it is.
	Data json.RawMessage `json:"data"`
	// Replay marks a REDELIVERY of an event already sent once — set only by the dispatcher's
	// redeliver, never by Emit. It travels as a header, not a body field: the envelope is a
	// compatibility contract and a fifth field would change the bytes every receiver already
	// verifies a signature over. Hence `json:"-"`.
	Replay bool `json:"-"`
}

// Listener receives an event. It is called INLINE on the emitter's goroutine and must return
// promptly: anything slow (a network send) belongs in a goroutine the listener starts, with its
// own recover — the bus only contains panics from the synchronous part.
type Listener func(e Event)

type entry struct {
	id int
	fn Listener
}

// Bus is the event bus.
//
// Owner: the package-level Default for the process; tests may construct their own. mu guards
// listeners, nextID and seq. listeners is copy-on-write — On and off publish a NEW slice, so Emit
// may iterate a snapshot taken under RLock without holding the lock (rule 14: no listener is ever
// called while the lock is held).
type Bus struct {
	mu        sync.RWMutex
	listeners []entry
	nextID    int
	seq       int64
}

// New returns an empty bus.
func New() *Bus { return &Bus{listeners: []entry{}} }

// On registers fn and returns the function that removes it. Listeners fire in registration order.
// The off function is idempotent.
func (b *Bus) On(fn Listener) (off func()) {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	// Full-slice expression forces append to allocate: a snapshot held by an in-flight Emit never
	// sees this write.
	b.listeners = append(b.listeners[:len(b.listeners):len(b.listeners)], entry{id: id, fn: fn})
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		next := make([]entry, 0, len(b.listeners))
		for _, l := range b.listeners {
			if l.id != id {
				next = append(next, l)
			}
		}
		b.listeners = next
	}
}

// Emit fires an event. It cannot panic out of a listener — by construction, not by convention.
//
// Every listener is called inside its own recover, so one bad subscriber cannot starve the others,
// and no subscriber can propagate into the caller. Call sites are inside `up`, `down` and request
// handlers; a panic escaping here would turn "the webhook is misconfigured" into "the deploy
// failed", which is the exact inversion this guards against.
//
// Dispatch is synchronous and in registration order: listeners run on the emitter's goroutine, so
// an emitter's events reach every listener in the order it emitted them — which is what gives the
// webhook dispatcher's per-notifier FIFO its meaning. Never `go fn(e)` here.
//
// data is marshalled once with jsonx; nil is `{}` (the TS default). A value jsonx cannot marshal is
// a programmer error at the emit site and panics — payloads are structs of primitives.
func (b *Bus) Emit(event string, data any) Event {
	var raw json.RawMessage
	if data == nil {
		raw = json.RawMessage("{}")
	} else {
		raw = jsonx.Must(data)
	}
	now := time.Now().UnixMilli()
	b.mu.Lock()
	b.seq++
	seq := b.seq
	b.mu.Unlock()
	e := Event{
		ID:    "evt_" + strconv.FormatInt(now, 36) + "_" + strconv.FormatInt(seq, 36) + "_" + randomBase36(6),
		Event: event,
		At:    now,
		Data:  raw,
	}
	b.mu.RLock()
	ls := b.listeners
	b.mu.RUnlock()
	for _, l := range ls {
		call(l.fn, e)
	}
	return e
}

// call is one listener invocation with its panic deliberately swallowed. A listener that panics is
// a bug in the listener; surfacing it here would punish the operation that merely happened to
// trigger it.
func call(fn Listener, e Event) {
	defer func() { _ = recover() }()
	fn(e)
}

const base36 = "0123456789abcdefghijklmnopqrstuvwxyz"

// randomBase36 is `Math.random().toString(36).slice(2, 8)`: n random base-36 digits. Not a
// security token — the id is a dedupe key, the random tail only keeps two processes apart.
func randomBase36(n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = base36[rand.IntN(36)]
	}
	return string(buf)
}

// Default is the process-wide bus.
//
// A package singleton rather than an injected dependency: emit sites are scattered through job
// execution and request handling, and threading a bus through `up`/`down`/`verify` and four compose
// helpers to reach them would be a large change for no testability gain — tests subscribe to this
// same instance and assert what arrived. A server subscribes its dispatcher with On and detaches it
// on stop, otherwise one event fans out into every database any server ever opened.
var Default = New()

// On is Default.On.
func On(fn Listener) (off func()) { return Default.On(fn) }

// Emit is Default.Emit.
func Emit(event string, data any) Event { return Default.Emit(event, data) }
