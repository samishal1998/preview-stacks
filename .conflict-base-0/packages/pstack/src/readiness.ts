/**
 * Did the stack actually come up?
 *
 * WHY THIS EXISTS. `compose up -d` returns as soon as the containers are *created*, so the deploy job
 * reports success while the app is still booting — and it reports the same success when the app boots,
 * throws, and exits two seconds later. Everything downstream (a CI step that curls the preview URL, an
 * operator watching the page, a webhook consumer that posts the link to a PR) then races the very
 * thing it is waiting for. The job answers "did the commands run"; nothing answered "is it serving".
 *
 * This is that second answer, and it is deliberately observational: it reads what Docker already
 * knows — container state, exit code, and the healthcheck the image declares — and narrates the
 * convergence. It starts nothing, restarts nothing and repairs nothing. A watcher that also acted
 * would be a second orchestrator racing compose's own restart policy.
 *
 * ── WHAT "READY" MEANS, AND WHY IT IS NOT ONE RULE ───────────────────────────────────────────────
 *
 * A container with a healthcheck is ready when Docker says `healthy`. A container WITHOUT one is
 * ready when it is simply running — the honest ceiling, because nothing else on this host knows what
 * "serving" means for that image. The distinction is reported per container (`hasHealthcheck`) rather
 * than hidden, so "ready" is never read as "probed" when nobody probed anything. This is the same
 * discipline as `verified` on a teardown: an unchecked thing says so.
 *
 * ── TERMINAL, ALWAYS, EXACTLY ONCE ───────────────────────────────────────────────────────────────
 *
 * Every watch ends in exactly one of `stack.ready`, `stack.failed`, `stack.timedout` — a consumer
 * that subscribes to all three never waits forever, which is the failure mode of an event stream that
 * only fires on success. The deadline is a real answer, not an absence of one.
 */

import { events } from './events.ts';
import { deploymentRuntime, type ContainerInfo } from './inspect.ts';
import type { Runner } from './exec.ts';
import type { Orchestrator } from './spec.ts';

export type ContainerReadiness = {
  name: string;
  /** The compose service, when the container carries the label. */
  service: string | null;
  /** Docker's own word: `running`, `exited`, `restarting`, `created`, … */
  state: string;
  /** `starting` / `healthy` / `unhealthy`, or null when the image declares no healthcheck. */
  health: string | null;
  /** Whether a healthcheck was ever observed — what separates "probed and passing" from "it is up". */
  hasHealthcheck: boolean;
  exitCode: number | null;
  /** Restarts Docker has performed. The only sample-independent evidence of a crash loop. */
  restartCount: number;
  ready: boolean;
  /** Terminal-bad: it exited non-zero, died, crash-looped, or its healthcheck reported unhealthy. */
  failed: boolean;
  /** Why it failed, in one line. Undefined while pending or ready. */
  reason?: string;
};

export type ReadinessState = 'watching' | 'ready' | 'failed' | 'timedout';

export type StackReadiness = {
  stack: string;
  state: ReadinessState;
  containers: ContainerReadiness[];
  startedAt: number;
  endedAt?: number;
  /** False when docker did not answer. Every container field is then "unknown", never "empty". */
  reachable: boolean;
  /** The deadline this watch was given, so a `timedout` answer can be read against it. */
  timeoutMs: number;
};

type Watch = {
  snap: StackReadiness;
  cancelled: boolean;
  /**
   * Whether this watch announces itself on the bus.
   *
   * FALSE for a watch a mere READ started. `GET …/readiness` on a deployment that was never
   * deployed finds no containers, converges on nothing, and 180 seconds later would emit
   * `stack.timedout` — so opening a freshly submitted deployment's page put "pr-9 did not become
   * ready in time" in someone's Slack. A read must not manufacture an event about something nobody
   * did; the caller still gets every state in the snapshot it asked for.
   */
  emit: boolean;
  /** Which labels name this stack's containers (inspect.ts). */
  orchestrator: Orchestrator;
  /** Last health string seen per container — the diff that drives `healthcheck.*`. */
  health: Map<string, string | null>;
  /** Containers that have already announced a per-container verdict; each announces once. */
  announced: Set<string>;
  waiters: Array<(s: StackReadiness) => void>;
};

/** Poll cadence. Two seconds is well under any realistic healthcheck interval and costs two docker calls. */
const POLL_MS = 2_000;
/** Long enough for an image pull plus a slow boot; short enough that a wedged stack is reported today. */
const DEFAULT_TIMEOUT_MS = 180_000;

/**
 * Restarts tolerated before a container is called a crash loop.
 *
 * Not 1: an app that boots before its database is accepting connections dies once and comes back
 * fine, and that pattern is everywhere in preview stacks — failing it would report half the world
 * broken. Not unbounded either, or a container with `restart: unless-stopped` that dies on every
 * boot is never reported at all: it cycles exited → restarting → running, so a 2s poll lands on a
 * "healthy-looking" sample about a third of the time and the watch runs out the clock instead of
 * naming the crash.
 */
const RESTART_LOOP = 3;

function readinessOf(c: ContainerInfo): ContainerReadiness {
  const hasHealthcheck = c.health !== null;
  const base = {
    name: c.name,
    service: c.service,
    state: c.state,
    health: c.health,
    hasHealthcheck,
    exitCode: c.exitCode,
    restartCount: c.restartCount,
  };
  if (c.health === 'unhealthy') {
    return { ...base, ready: false, failed: true, reason: 'healthcheck reports unhealthy' };
  }
  // A PASSING healthcheck outranks the restart counter below: it is evidence about now, the counter
  // is evidence about the past, and a container that crash-looped and then came good is good.
  if (c.state === 'running' && c.health === 'healthy') return { ...base, ready: true, failed: false };
  if (c.restartCount >= RESTART_LOOP) {
    return {
      ...base,
      ready: false,
      failed: true,
      reason: `restarted ${c.restartCount} times — crash loop${c.exitCode ? ` (last exit ${c.exitCode})` : ''}`,
    };
  }
  // A container that exited 0 is NOT a failure: one-shot migration and seed services are supposed to
  // finish. Calling that a crash would make every stack with a migration step permanently "failed".
  if (c.state === 'exited') {
    return c.exitCode === 0
      ? { ...base, ready: true, failed: false }
      : { ...base, ready: false, failed: true, reason: `exited with code ${c.exitCode ?? 'unknown'}` };
  }
  if (c.state === 'dead') return { ...base, ready: false, failed: true, reason: 'container is dead' };
  if (c.state === 'running' && c.health === null) return { ...base, ready: true, failed: false };
  // created / restarting / paused / running-but-starting: still converging.
  return { ...base, ready: false, failed: false };
}

/**
 * Watches, one per stack.
 *
 * Held by the server (not a module singleton) so stopping a server stops its timers — the same reason
 * the event dispatcher is per-server. A leaked poll loop in a test run is an invisible docker call
 * every two seconds for the life of the process.
 */
export class ReadinessWatcher {
  #byStack = new Map<string, Watch>();
  #pollMs: number;
  #defaultTimeoutMs: number;

  constructor(opts: { pollMs?: number; timeoutMs?: number } = {}) {
    this.#pollMs = opts.pollMs ?? POLL_MS;
    this.#defaultTimeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  }

  get(stack: string): StackReadiness | undefined {
    return this.#byStack.get(stack)?.snap;
  }

  /**
   * Start watching, or return the watch already running.
   *
   * A FINISHED watch is replaced, not returned: asking again after a deploy means "how is it NOW",
   * and handing back last hour's verdict is how a page shows a torn-down stack as ready.
   */
  start(
    stack: string,
    runner: Runner,
    opts: { timeoutMs?: number; restart?: boolean; emit?: boolean; orchestrator?: Orchestrator } = {},
  ): StackReadiness {
    const existing = this.#byStack.get(stack);
    if (existing && existing.snap.state === 'watching' && !opts.restart) return existing.snap;

    existing?.waiters.splice(0).forEach((fn) => fn(existing.snap));
    if (existing) existing.cancelled = true;

    const timeoutMs = opts.timeoutMs ?? this.#defaultTimeoutMs;
    const w: Watch = {
      snap: {
        stack,
        state: 'watching',
        containers: [],
        startedAt: Date.now(),
        reachable: true,
        timeoutMs,
      },
      cancelled: false,
      orchestrator: opts.orchestrator ?? 'compose',
      emit: opts.emit ?? true,
      health: new Map(),
      announced: new Set(),
      waiters: [],
    };
    this.#byStack.set(stack, w);
    void this.#loop(w, runner);
    return w.snap;
  }

  /**
   * Resolve when the watch reaches a terminal state, or when `ms` elapses — the long-poll behind
   * `GET …/readiness?wait=30`.
   *
   * Returning the CURRENT snapshot on expiry rather than an error is what makes a client loop trivial:
   * re-issue while `state === 'watching'`, stop otherwise. There is no missed-edge race, because the
   * snapshot carries the state rather than only the transition.
   */
  wait(stack: string, ms: number): Promise<StackReadiness | undefined> {
    const w = this.#byStack.get(stack);
    if (!w || w.snap.state !== 'watching') return Promise.resolve(w?.snap);
    return new Promise((resolve) => {
      let done = false;
      const finish = (s: StackReadiness | undefined) => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        resolve(s);
      };
      const timer = setTimeout(() => finish(w.snap), ms);
      // Node/Bun keep the process alive for a pending timer; a long-poll must never do that.
      (timer as unknown as { unref?: () => void }).unref?.();
      w.waiters.push(finish);
    });
  }

  /** Stop watching — a teardown makes every pending readiness question moot. */
  cancel(stack: string): void {
    const w = this.#byStack.get(stack);
    if (!w) return;
    w.cancelled = true;
    this.#byStack.delete(stack);
    w.waiters.splice(0).forEach((fn) => fn(w.snap));
  }

  stopAll(): void {
    for (const stack of [...this.#byStack.keys()]) this.cancel(stack);
  }

  async #loop(w: Watch, runner: Runner): Promise<void> {
    while (!w.cancelled && w.snap.state === 'watching') {
      await this.#tick(w, runner);
      if (w.cancelled || w.snap.state !== 'watching') break;
      if (Date.now() - w.snap.startedAt >= w.snap.timeoutMs) {
        this.#settle(w, 'timedout');
        break;
      }
      await Bun.sleep(this.#pollMs);
    }
  }

  async #tick(w: Watch, runner: Runner): Promise<void> {
    const rt = await deploymentRuntime({ stack: w.snap.stack, runner, challenge: 'unknown', orchestrator: w.orchestrator });
    if (w.cancelled) return;

    // Docker did not answer. Report it and keep watching: a momentary daemon hiccup is not a verdict,
    // and the deadline is what stops this from waiting forever.
    if (!rt.reachable) {
      w.snap.reachable = false;
      return;
    }
    w.snap.reachable = true;
    w.snap.containers = rt.containers.map(readinessOf);

    for (const c of w.snap.containers) {
      this.#healthEvents(w, c);
      if (w.announced.has(c.name)) continue;
      if (c.failed) {
        w.announced.add(c.name);
        if (w.emit) events.emit('container.start-failed', {
          stack: w.snap.stack,
          container: c.name,
          service: c.service,
          state: c.state,
          health: c.health,
          exitCode: c.exitCode,
          reason: c.reason,
        });
      } else if (c.ready) {
        w.announced.add(c.name);
        if (w.emit) events.emit('container.ready', {
          stack: w.snap.stack,
          container: c.name,
          service: c.service,
          state: c.state,
          health: c.health,
          hasHealthcheck: c.hasHealthcheck,
        });
      }
    }

    if (w.snap.containers.some((c) => c.failed)) return this.#settle(w, 'failed');
    // `length > 0` matters: compose creates containers a moment after `up` returns, and an empty list
    // read as "all ready" would report every stack ready before it had started.
    if (w.snap.containers.length > 0 && w.snap.containers.every((c) => c.ready)) {
      return this.#settle(w, 'ready');
    }
  }

  /** `healthcheck.*` for one container, driven by the change in Docker's own status string. */
  #healthEvents(w: Watch, c: ContainerReadiness): void {
    const seen = w.health.has(c.name);
    const prev = w.health.get(c.name) ?? null;
    if (seen && prev === c.health) return;
    w.health.set(c.name, c.health);
    if (c.health === null || !w.emit) return; // no healthcheck to narrate, or a silent watch

    const base = {
      stack: w.snap.stack,
      container: c.name,
      service: c.service,
      status: c.health,
    };
    if (!seen || prev === null) events.emit('healthcheck.started', base);
    else events.emit('healthcheck.updated', { ...base, previous: prev });
    // `healthy`/`unhealthy` are terminal for the PROBE even when the container keeps running, so this
    // fires alongside `started` when the very first observation is already settled — the honest report.
    if (c.health === 'healthy' || c.health === 'unhealthy') {
      events.emit('healthcheck.finished', { ...base, healthy: c.health === 'healthy' });
    }
  }

  #settle(w: Watch, state: Exclude<ReadinessState, 'watching'>): void {
    if (w.snap.state !== 'watching') return;
    w.snap.state = state;
    w.snap.endedAt = Date.now();

    if (state === 'timedout' && w.emit) {
      // Name what was still in flight when the deadline hit. "Timed out" without the list is a
      // support thread; with it, it is a container name to go and look at.
      for (const c of w.snap.containers) {
        if (c.health === 'starting') {
          events.emit('healthcheck.timedout', {
            stack: w.snap.stack,
            container: c.name,
            service: c.service,
            status: c.health,
            waitedMs: w.snap.endedAt - w.snap.startedAt,
          });
        }
      }
    }

    const pending = w.snap.containers.filter((c) => !c.ready && !c.failed).map((c) => c.name);
    const failed = w.snap.containers.filter((c) => c.failed).map((c) => c.name);
    if (w.emit) events.emit(
      state === 'ready' ? 'stack.ready' : state === 'failed' ? 'stack.failed' : 'stack.timedout',
      {
        stack: w.snap.stack,
        state,
        containers: w.snap.containers.length,
        ready: w.snap.containers.filter((c) => c.ready).length,
        failedContainers: failed,
        pendingContainers: pending,
        durationMs: w.snap.endedAt - w.snap.startedAt,
        reachable: w.snap.reachable,
      },
    );
    w.waiters.splice(0).forEach((fn) => fn(w.snap));
  }
}
