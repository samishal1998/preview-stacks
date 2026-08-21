/**
 * In-memory job registry.
 *
 * `up`/`down` take minutes, so the HTTP API cannot answer them synchronously: a browser or CI
 * client would sit on an open socket through an image build. Instead a POST starts a job and
 * returns its id; the client polls or subscribes to the log stream.
 *
 * Deliberately in-memory and unpersisted, matching the CLI's "no state store" rule. Truth about
 * what exists lives in Docker and in each axis's own `assert_*` probe — a job record is just the
 * transcript of an attempt. Restarting the server loses history, not correctness.
 */

import type { Outcome } from './stack.ts';
import { bufferSink, type LogEvent, type Sink } from './log.ts';
import { events } from './events.ts';
import { redactText } from './redact.ts';

/**
 * `sleep` and `wake` are the scheduler's verbs (stack.ts `sleep`; wake IS `up`, recorded under its own
 * name so a transcript says why the stack came back). They hold the same per-stack lock as the rest:
 * a wake racing a `down` over one database branch is the corruption the lock exists to prevent.
 */
export type JobAction = 'up' | 'down' | 'verify' | 'sleep' | 'wake';
/**
 * `cancelled` is its OWN state, not a flavour of `failed`.
 *
 * A failed job tried and did not succeed; a cancelled one was stopped part-way by a person, and
 * what it had already created or destroyed is still out there. Collapsing the two would let a
 * half-torn-down stack read as "the teardown failed, retry it" when the honest report is "someone
 * stopped this and nobody has looked since".
 */
export type JobState = 'running' | 'ok' | 'failed' | 'leaked' | 'cancelled';

export type Job = {
  id: string;
  stack: string;
  action: JobAction;
  state: JobState;
  startedAt: number;
  endedAt?: number;
  outcome?: Outcome;
  error?: string;
  log: LogEvent[];
  /** Who stopped it, when it was stopped. Absent on every other path. */
  cancelledBy?: string;
};

/** Keep the last N job transcripts. Bounded so a long-lived server cannot grow without limit. */
const MAX_JOBS = 50;

export class JobRegistry {
  #jobs = new Map<string, Job>();
  /**
   * One in-flight job per stack. Concurrent `up` and `down` on the same stack would race over the
   * same compose project and the same external resources — a `down` deleting the database branch an
   * `up` just created is exactly the kind of corruption that is hard to diagnose afterwards.
   */
  #locks = new Set<string>();
  #subscribers = new Map<string, Set<(e: LogEvent) => void>>();
  #seq = 0;
  /** Per-job kill switch. Present only while the job runs; deleted in the same `finally` as the lock. */
  #aborts = new Map<string, AbortController>();

  list(): Job[] {
    return [...this.#jobs.values()].sort((a, b) => b.startedAt - a.startedAt);
  }

  get(id: string): Job | undefined {
    return this.#jobs.get(id);
  }

  isBusy(stack: string): boolean {
    return this.#locks.has(stack);
  }

  /**
   * Start a job. Returns the job, or null when that stack already has one in flight — the caller
   * should surface that as HTTP 409 rather than queueing, so the operator sees the conflict.
   */
  /**
   * Stop a running job.
   *
   * Returns false when there is nothing to stop — an unknown id, or a job that already finished.
   * The caller should say so rather than reporting success, because "it was already done" and "it
   * has been stopped" are different facts about the infrastructure.
   *
   * What this does NOT do is undo anything. The abort reaches the current shell command; every
   * resource created before it stays created. That is why the state is `cancelled` and why the API
   * tells the operator to verify afterwards.
   */
  cancel(id: string, by: string): boolean {
    const job = this.#jobs.get(id);
    const abort = this.#aborts.get(id);
    if (!job || !abort || job.state !== 'running') return false;
    job.cancelledBy = by;
    abort.abort();
    return true;
  }

  start(
    stack: string,
    action: JobAction,
    /**
     * The work, given a sink to narrate into and a signal to stop for. The signal is what a caller
     * must thread into its `Runner`, or `cancel` will flip the state while the shell keeps running.
     */
    work: (sink: Sink, signal: AbortSignal) => Promise<Outcome>,
    /**
     * Applied to every string that lands in the RECORD: outcome step messages, captured outputs and
     * the crash error. The live sink is the caller's to wrap, but a failing hook's stderr goes into
     * `StepResult.message` — the outcome, not the sink — and the record is what GET /api/jobs/:id
     * serves forever after.
     */
    scrub: (text: string) => string = (s) => s,
  ): Job | null {
    if (this.#locks.has(stack)) return null;
    this.#locks.add(stack);
    const abort = new AbortController();

    const id = `${action}-${stack}-${++this.#seq}-${Math.random().toString(36).slice(2, 8)}`;
    const sink = bufferSink((e) => this.#fanout(id, e));
    const job: Job = {
      id,
      stack,
      action,
      state: 'running',
      startedAt: Date.now(),
      log: sink.events,
    };
    this.#jobs.set(id, job);
    this.#aborts.set(id, abort);
    this.#evict();

    events.emit('job.started', { jobId: id, stack, action, startedAt: job.startedAt });

    // Fire and forget: the HTTP handler returns immediately with the job id.
    void (async () => {
      try {
        const outcome = await work(sink, abort.signal);
        job.outcome = {
          ...outcome,
          steps: outcome.steps.map((s) => (s.message ? { ...s, message: scrub(s.message) } : s)),
          // `outputs` is the documented inter-axis credential channel (docs/secret-exposure.md);
          // host secrets echoed into it must not survive into the served record.
          outputs: Object.fromEntries(Object.entries(outcome.outputs).map(([k, v]) => [k, scrub(v)])),
        };
        const leaked = outcome.steps.some((s) => s.phase === 'assert_gone' && !s.ok);
        // Cancellation wins over the outcome it produced. A stopped run reports non-ok steps by
        // construction (the killed command exits non-zero), and calling that `failed` would say the
        // work was attempted and did not succeed — it was stopped, which is a different report.
        job.state = abort.signal.aborted
          ? 'cancelled'
          : leaked
            ? 'leaked'
            : outcome.ok
              ? 'ok'
              : 'failed';
      } catch (err) {
        job.state = abort.signal.aborted ? 'cancelled' : 'failed';
        job.error = scrub((err as Error).message);
        sink.emit('error', `job crashed: ${job.error}`);
      } finally {
        job.endedAt = Date.now();
        if (job.state === 'cancelled') {
          sink.emit(
            'error',
            `cancelled by ${job.cancelledBy ?? 'an operator'} — whatever ran before this point ` +
              `was NOT undone. Run verify to see what exists.`,
          );
        }
        this.#locks.delete(stack);
        this.#aborts.delete(id);
        // Wake any SSE stream so it can observe the terminal state and close.
        this.#fanout(id, { seq: -1, at: Date.now(), level: 'info', message: '__end__' });

        // THE choke point: success, failure, leaked and crash all converge here with `state` already
        // assigned. Emitted AFTER the lock is released, matching the `__end__` sentinel above — a
        // listener that reacts by starting a follow-on job would otherwise hit its own 409.
        //
        // The payload is built FIELD BY FIELD and deliberately excludes `outcome`: `outcome.outputs`
        // is the inter-axis credential channel (a provisioned database's connection string lives
        // there by design) and a webhook URL is outside the auth gate that protects every other
        // consumer of job data. Only identity and status leave the process.
        const leakedAxes =
          job.outcome?.steps
            .filter((s) => s.phase === 'assert_gone' && !s.ok)
            .map((s) => s.axis) ?? [];
        // `outcome` is UNDEFINED on the crash path, so every read of it is optional — an unguarded
        // `job.outcome.steps` here would throw inside this very `finally` and swallow the failure it
        // was meant to report.
        const unverifiable =
          job.outcome?.steps.filter((s) => s.message?.startsWith('unverifiable')).length ?? 0;
        events.emit(
          job.state === 'leaked'
            ? 'job.leaked'
            : job.state === 'ok'
              ? 'job.succeeded'
              : job.state === 'cancelled'
                ? 'job.cancelled'
                : 'job.failed',
          {
            jobId: job.id,
            stack: job.stack,
            action: job.action,
            state: job.state,
            startedAt: job.startedAt,
            endedAt: job.endedAt,
            durationMs: job.endedAt - job.startedAt,
            // Named axes are the operator-actionable part of a leak.
            leakedAxes,
            /**
             * Whether teardown was actually PROVEN — and `null` where the question does not apply.
             *
             * `down` with `verify: false` emits zero `assert_gone` steps, and a spec whose axes define
             * none yields all-skipped ones; in both cases the job reports success having checked
             * nothing, so `false` is the honest answer and stops `job.succeeded` reading as "clean".
             *
             * But an `up` runs no `assert_gone` by design, so reporting `false` there would claim a
             * failure to verify something that was never in question. Three-valued deliberately:
             * `true` proven, `false` nobody looked, `null` not applicable to this action.
             */
            verified:
              job.action === 'up'
                ? null
                : (job.outcome?.steps.some((s) => s.phase === 'assert_gone' && !s.skipped) ?? false),
            unverifiable,
            cancelledBy: job.cancelledBy,
            // First line only, and only on the crash path. Hook stderr can carry a credential, so it
            // is redacted the way container logs are.
            error: job.error ? redactText(job.error).split('\n')[0]!.slice(0, 300) : undefined,
          },
        );
      }
    })();

    return job;
  }

  /**
   * Deliver to every subscriber of a job, defensively.
   *
   * Each callback is isolated. This fans out from inside the job's `finally`, so an exception
   * escaping here aborts job cleanup — that is exactly what happened when a cancelled SSE stream
   * left a subscription enqueueing into a closed controller. A subscriber is a listener, and a
   * listener's failure is never the emitting operation's problem.
   */
  #fanout(id: string, e: LogEvent): void {
    for (const fn of this.#subscribers.get(id) ?? []) {
      try {
        fn(e);
      } catch {
        /* a broken listener must not break the job */
      }
    }
  }

  subscribe(id: string, fn: (e: LogEvent) => void): () => void {
    let set = this.#subscribers.get(id);
    if (!set) this.#subscribers.set(id, (set = new Set()));
    set.add(fn);
    return () => {
      set!.delete(fn);
      if (set!.size === 0) this.#subscribers.delete(id);
    };
  }

  #evict(): void {
    if (this.#jobs.size <= MAX_JOBS) return;
    // Never evict a running job, however old.
    const done = this.list().filter((j) => j.state !== 'running');
    for (const j of done.slice(MAX_JOBS - 1)) this.#jobs.delete(j.id);
  }
}
