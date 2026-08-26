/**
 * The queue facts the wire does not carry: why a job is still waiting, and what replaced it.
 *
 * A job record has a state and nothing about the queue it sits in — there is no `waitingOn` and no
 * `supersededBy`. Both are DERIVED here, from the job list the shell already polls every seven
 * seconds, rather than asked for as server fields that would exist only to spare this app an array
 * scan. Pure functions, same shape as `useSteps`, so a view can call them in a computed.
 *
 * WHY THE TWO WAITS ARE WORTH TELLING APART. "Behind its own stack" is the product working exactly
 * as designed — one job per stack is the guarantee — and the fix is to wait or stop the job ahead.
 * "The host is at its job cap" is a capacity fact about the whole machine, and the fix is
 * `PSTACK_MAX_JOBS` or fewer concurrent deploys. An operator debugging a slow deploy is asking
 * which of those two it is, and a single word "queued" answers neither.
 *
 * WHEN THE LIST IS EMPTY THEY SAY SO. A deep link to `/jobs/:id` before the first poll lands, or a
 * share-link page that never polls, has nothing to reason over. `unknown` renders as an unqualified
 * "waiting its turn" — true — rather than asserting a cap nobody measured.
 */
import type { Job, JobState } from '../api/types';

/** Terminal is forever: the server's `State.Terminal()`, restated. NOT `state !== 'running'`. */
export function isTerminal(state: JobState): boolean {
  return state !== 'queued' && state !== 'running';
}

/** Why a queued job has not started. `running` is how many jobs hold a slot host-wide. */
export type Wait =
  | { kind: 'stack'; blocker: Job }
  | { kind: 'slot'; running: number }
  | { kind: 'unknown' };

export function waitReason(job: Job, jobs: Job[]): Wait {
  // Everything EXCEPT itself. Today's callers only ask about a queued job, which is never running,
  // but without this a running job asked why it waits answers "behind itself" — a banner linking to
  // the page you are already on, and a slot count that includes the slot it holds. A predicate only
  // correct for the two call sites that exist today is a trap for the third.
  const others = jobs.filter((j) => j.id !== job.id);
  const blocker = others.find((j) => j.state === 'running' && j.stack === job.stack);
  if (blocker) return { kind: 'stack', blocker };
  const running = others.filter((j) => j.state === 'running').length;
  // ponytail: the cap itself is not on the wire, so "the host is full" is inferred from other
  // stacks holding slots. A poll landing in the second between a slot freeing and the dispatch
  // shows this once and corrects itself on the next tick; serving `maxRunning` on /api/health is
  // the upgrade if that ever misleads anyone.
  return running > 0 ? { kind: 'slot', running } : { kind: 'unknown' };
}

/**
 * The job that replaced a superseded one, or null.
 *
 * The list is newest-first and the queue is ONE deep, so the nearest newer job for the same stack
 * is necessarily the one that took its place — there cannot have been another in between. Null
 * when the successor has aged out of the 50 kept transcripts, or when the list has not loaded;
 * a missing link is better than a wrong one.
 */
export function supersededBy(job: Job, jobs: Job[]): Job | null {
  const i = jobs.findIndex((j) => j.id === job.id);
  if (i < 0) return null;
  // Newest-first, so everything before it is newer; the nearest of those on the same stack wins.
  const newer = jobs.slice(0, i).reverse();
  return newer.find((j) => j.stack === job.stack) ?? null;
}

/**
 * Why the host's concurrency limit cannot be saved as typed, or `''` when it can.
 *
 * Takes `string | number` because that is what the box actually holds. `v-model` on
 * `<input type="number">` hands back a NUMBER the moment the value parses — Vue casts for that
 * input TYPE, with or without the `.number` modifier — while an empty box and the value first read
 * from the server are strings. Assuming either one throws on the other, which is exactly how this
 * arrived: `.trim()` on the number the first keystroke produced.
 *
 * The server takes an integer ≥ 1 and refuses everything else; saying so here spares a round trip
 * and never replaces it — the 403 or 400 is still the enforcement.
 */
export function capProblem(draft: string | number): string {
  const raw = String(draft).trim();
  if (!raw) return 'type a number first';
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1) return 'the limit is a whole number, 1 or more';
  return '';
}
