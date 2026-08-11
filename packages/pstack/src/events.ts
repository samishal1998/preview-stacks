/**
 * The domain event bus.
 *
 * One narrow job: something happened, tell whoever registered interest, and NEVER let a listener's
 * failure reach the thing that happened. A webhook endpoint that is down, slow, or throwing must not
 * fail a deploy — so `emit` is synchronous-dispatch/asynchronous-delivery: it hands each listener the
 * event and returns immediately, and every listener call is individually wrapped.
 *
 * WHY THIS EXISTS SEPARATELY from `JobRegistry`'s subscriber set: that one carries LOG LINES for one
 * job to one SSE stream — a transport detail with a per-job lifetime. This carries DOMAIN events for
 * the whole process with a process lifetime, and its consumers (webhooks now; Slack, Discord, email
 * later) care about "a teardown leaked", not about individual log lines.
 *
 * ── THE EVENT NAMES ARE A PUBLIC CONTRACT ────────────────────────────────────────────────────────
 *
 * They go into stored webhook registrations, so renaming one silently stops deliveries for everyone
 * who subscribed to it. Add; do not rename. `EVENTS` is the enumeration a UI offers and the API
 * validates against — an unknown name in a registration is refused at write time rather than
 * discovered as silence months later.
 */

export const EVENTS = [
  'deployment.created',
  'deployment.updated',
  'deployment.deleted',
  'job.started',
  'job.succeeded',
  'job.failed',
  /**
   * A person stopped it part-way. Distinct from `job.failed` for the same reason `job.leaked` is:
   * a cancelled run did not try and lose, it was interrupted, and whatever it had already created
   * or destroyed is still that way with nobody watching.
   */
  'job.cancelled',
  /**
   * The one this product exists for: teardown ran and something survived it. Distinct from
   * `job.failed` — a failed job did not do what it was told, a leaked one left a resource behind
   * that nothing else will clean up.
   */
  'job.leaked',
  'spec.stored',
  'spec.deleted',
  'routing.changed',
  /**
   * Readiness convergence, observed by the watcher that starts after every successful deploy (see
   * readiness.ts). `compose up -d` returning is not "the app works": containers still have to pass
   * their healthchecks, and a crash-on-boot exits *after* the deploy job already reported success.
   * These events carry that second half.
   *
   * The `healthcheck.*` family narrates one container's health probe: `started` when a probe is
   * first observed, `updated` on every status change, `finished` when it settles (healthy or
   * unhealthy), `timedout` when the watcher gave up while it was still starting. `container.*` is
   * the per-container verdict; `stack.*` the aggregate one — exactly one of `stack.ready`,
   * `stack.failed`, `stack.timedout` ends each watch.
   */
  'healthcheck.started',
  'healthcheck.updated',
  'healthcheck.finished',
  'healthcheck.timedout',
  'container.ready',
  'container.start-failed',
  /**
   * A person acted on ONE container through the API (`POST …/containers/:name/(start|stop|restart)`).
   * Separate from the readiness family above, which observes what docker did on its own: these say
   * somebody chose it, and carry `by`.
   */
  'container.started',
  'container.stopped',
  'container.restarted',
  'stack.ready',
  'stack.failed',
  'stack.timedout',
] as const;

export type EventName = (typeof EVENTS)[number];

export function isEventName(v: unknown): v is EventName {
  return typeof v === 'string' && (EVENTS as readonly string[]).includes(v);
}

/**
 * `'*'` subscribes to everything, including events added in later versions.
 *
 * One line here spares every operator from re-registering each time the enumeration grows — which is
 * the failure mode of a strict enum: the feature works, nobody is notified, and nobody knows why.
 */
export const WILDCARD = '*';
export function isSubscribable(v: unknown): v is EventName | typeof WILDCARD {
  return v === WILDCARD || isEventName(v);
}

export type PstackEvent = {
  /** Unique per emission. A receiver dedupes on this across retries. */
  id: string;
  event: EventName;
  /** Epoch ms. Signed along with the body, so a receiver can reject a stale replay. */
  at: number;
  /**
   * Event-specific detail. Deliberately loose here and shaped at each emit site: a schema per event
   * would be ceremony for a payload whose only consumer is JSON on the wire.
   *
   * NOTHING SECRET GOES IN. Job outcomes carry captured credentials by design (`outcome.outputs` is
   * the inter-axis env channel), so emit sites send identity and status — ids, stack, state, counts —
   * and never the outcome object. See `docs/secret-exposure.md` for why that channel is the way it is.
   */
  data: Record<string, unknown>;
  /**
   * This is a REDELIVERY of an event already sent once — set only by `Dispatcher.redeliver`, never
   * by `emit`. It travels as a header, not a body field: the envelope is a compatibility contract
   * and a fifth field would change the bytes every receiver already verifies a signature over.
   */
  replay?: boolean;
};

/** May be async — the bus handles a returned promise's rejection; see `emit`. */
export type Listener = (e: PstackEvent) => void | Promise<void>;

export class EventBus {
  #listeners = new Set<Listener>();
  #seq = 0;

  on(fn: Listener): () => void {
    this.#listeners.add(fn);
    return () => this.#listeners.delete(fn);
  }

  /**
   * Fire an event. Returns void and cannot throw — by construction, not by convention.
   *
   * Every listener is called inside its own try/catch, so one bad subscriber cannot starve the
   * others, and no subscriber can propagate into the caller. Call sites are inside `up`, `down` and
   * request handlers; an exception escaping here would turn "the webhook is misconfigured" into
   * "the deploy failed", which is the exact inversion this guards against.
   */
  emit(event: EventName, data: Record<string, unknown> = {}): PstackEvent {
    const e: PstackEvent = {
      id: `evt_${Date.now().toString(36)}_${(++this.#seq).toString(36)}_${Math.random().toString(36).slice(2, 8)}`,
      event,
      at: Date.now(),
      data,
    };
    for (const fn of this.#listeners) {
      try {
        // `Promise.resolve(...)` is load-bearing, not defensive noise: a bare `try/catch` catches
        // NOTHING from `async (e) => …` — the rejection escapes as an unhandled rejection and, with
        // Bun's strict mode, takes down the server that was merely doing a deploy. Both halves are
        // needed: the catch for a synchronous throw, the `.catch` for an asynchronous one.
        void Promise.resolve(fn(e)).catch(() => {});
      } catch {
        // Deliberately swallowed. A listener that throws is a bug in the listener; surfacing it here
        // would punish the operation that merely happened to trigger it.
      }
    }
    return e;
  }
}

/**
 * The process-wide bus.
 *
 * A module singleton rather than an injected dependency: emit sites are scattered through job
 * execution and request handling, and threading a bus through `up`/`down`/`verify` and four compose
 * helpers to reach them would be a large change for no testability gain — tests subscribe to this
 * same instance and assert what arrived.
 */
export const events = new EventBus();
