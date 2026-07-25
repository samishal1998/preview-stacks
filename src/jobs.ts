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

export type JobAction = 'up' | 'down' | 'verify';
export type JobState = 'running' | 'ok' | 'failed' | 'leaked';

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
  start(
    stack: string,
    action: JobAction,
    work: (sink: Sink) => Promise<Outcome>,
  ): Job | null {
    if (this.#locks.has(stack)) return null;
    this.#locks.add(stack);

    const id = `${action}-${stack}-${++this.#seq}-${Math.random().toString(36).slice(2, 8)}`;
    const sink = bufferSink((e) => {
      for (const fn of this.#subscribers.get(id) ?? []) fn(e);
    });
    const job: Job = {
      id,
      stack,
      action,
      state: 'running',
      startedAt: Date.now(),
      log: sink.events,
    };
    this.#jobs.set(id, job);
    this.#evict();

    // Fire and forget: the HTTP handler returns immediately with the job id.
    void (async () => {
      try {
        const outcome = await work(sink);
        job.outcome = outcome;
        const leaked = outcome.steps.some((s) => s.phase === 'assert_gone' && !s.ok);
        job.state = leaked ? 'leaked' : outcome.ok ? 'ok' : 'failed';
      } catch (err) {
        job.state = 'failed';
        job.error = (err as Error).message;
        sink.emit('error', `job crashed: ${job.error}`);
      } finally {
        job.endedAt = Date.now();
        this.#locks.delete(stack);
        // Wake any SSE stream so it can observe the terminal state and close.
        for (const fn of this.#subscribers.get(id) ?? []) {
          fn({ seq: -1, at: Date.now(), level: 'info', message: '__end__' });
        }
      }
    })();

    return job;
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
