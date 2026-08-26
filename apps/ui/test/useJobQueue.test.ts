/**
 * The queue derivations, tested directly — there is no component harness in this app, so the logic
 * that would otherwise be inline in a `.vue` lives in a `.ts` and is proved here.
 *
 * Outside `src/` on purpose, exactly like `useVarFormats.test.ts`: `tsconfig.app.json` includes
 * `src/**​/*.ts`, and a `bun:test` import there would fail `vue-tsc` unless `@types/bun` joined the
 * app's dependencies for a file the app never ships.
 *
 * Run with `cd apps/ui && bun test test/useJobQueue.test.ts`.
 */
import { describe, expect, test } from 'bun:test';
import type { Job, JobState } from '../src/api/types';
import { capProblem, isTerminal, supersededBy, waitReason } from '../src/composables/useJobQueue';

/** A job list is newest-first, the way `GET /api/jobs` serves it. */
function job(id: string, stack: string, state: JobState): Job {
  return { id, stack, action: 'up', state, startedAt: state === 'queued' ? null : 1 };
}

describe('isTerminal', () => {
  test('queued and running are not terminal; everything else is', () => {
    // negative control: `return state !== 'running'` — queued reports terminal, and the job page
    // then never opens a stream for the one state whose stream is the only thing that moves it.
    expect(isTerminal('queued')).toBe(false);
    expect(isTerminal('running')).toBe(false);
    for (const s of ['ok', 'failed', 'leaked', 'cancelled', 'superseded'] as const) {
      expect(isTerminal(s)).toBe(true);
    }
  });
});

describe('waitReason', () => {
  test('a running job on the same stack is the blocker, and it is named', () => {
    // negative control: drop `j.state === 'running'` from the find — a FINISHED job on the stack
    // becomes the blocker, and the page links the operator to a job that ended an hour ago.
    const mine = job('q', 'pr-7', 'queued');
    const jobs = [mine, job('old', 'pr-7', 'ok'), job('run', 'pr-7', 'running')];
    const w = waitReason(mine, jobs);
    expect(w.kind).toBe('stack');
    expect(w.kind === 'stack' && w.blocker.id).toBe('run');
  });

  test('a job is never its own blocker', () => {
    // negative control: drop `j.id !== job.id` — a running job asked why it waits answers "behind
    // itself", and the banner links to the page the operator is already looking at.
    const mine = job('run', 'pr-7', 'running');
    expect(waitReason(mine, [mine]).kind).toBe('unknown');
    expect(waitReason(mine, [mine, job('other', 'pr-9', 'running')]).kind).toBe('slot');
  });

  test('nothing on this stack but jobs elsewhere means the host cap, counted', () => {
    // negative control: `return { kind: 'slot', running }` unconditionally — the empty-list case
    // below then claims a cap that nothing measured.
    const mine = job('q', 'pr-7', 'queued');
    const jobs = [
      mine,
      job('a', 'pr-1', 'running'),
      job('b', 'pr-2', 'running'),
      job('c', 'pr-3', 'ok'),
    ];
    const w = waitReason(mine, jobs);
    expect(w.kind).toBe('slot');
    expect(w.kind === 'slot' && w.running).toBe(2);
  });

  test('an unloaded or idle list says unknown rather than guessing', () => {
    const mine = job('q', 'pr-7', 'queued');
    expect(waitReason(mine, []).kind).toBe('unknown');
    expect(waitReason(mine, [mine, job('c', 'pr-3', 'ok')]).kind).toBe('unknown');
  });
});

describe('supersededBy', () => {
  test('the NEAREST newer job on the same stack replaced it', () => {
    // negative control: drop `.reverse()` — the find then returns the newest same-stack job in the
    // whole list ('newest'), not the one that actually took this job's place.
    const mine = job('mine', 'pr-7', 'superseded');
    const jobs = [
      job('newest', 'pr-7', 'queued'),
      job('nearest', 'pr-7', 'superseded'),
      mine,
      job('older', 'pr-7', 'running'),
    ];
    expect(supersededBy(mine, jobs)?.id).toBe('nearest');
  });

  test('another stack never counts as the replacement', () => {
    // negative control: drop the `j.stack === job.stack` predicate — a deploy of an unrelated
    // deployment is presented as "the newer job that replaced this one".
    const mine = job('mine', 'pr-7', 'superseded');
    const jobs = [job('other', 'pr-9', 'running'), mine, job('older', 'pr-7', 'ok')];
    expect(supersededBy(mine, jobs)).toBeNull();
  });

  test('null when the successor aged out, and when the list has not loaded', () => {
    const mine = job('mine', 'pr-7', 'superseded');
    expect(supersededBy(mine, [mine])).toBeNull();
    expect(supersededBy(mine, [])).toBeNull();
  });
});

describe('capProblem', () => {
  test('a NUMBER is accepted — `v-model` on a number input stops handing back strings', () => {
    // negative control: `draft.trim()` instead of `String(draft).trim()` — every one of these
    // throws `trim is not a function`, which is the TypeError this shipped as in 0.33.0. The
    // Settings page renders this in a computed, so the whole panel died on the first keystroke.
    expect(capProblem(4)).toBe('');
    expect(capProblem(1)).toBe('');
    expect(capProblem(0)).toBe('the limit is a whole number, 1 or more');
    expect(capProblem(2.5)).toBe('the limit is a whole number, 1 or more');
  });

  test('an empty box asks for a number rather than refusing one', () => {
    // negative control: drop the `!raw` arm — `Number('')` is 0, so an untouched box reports the
    // integer complaint about a value nobody typed.
    expect(capProblem('')).toBe('type a number first');
    expect(capProblem('   ')).toBe('type a number first');
  });

  test('what the server would refuse is refused here first', () => {
    // negative control: `n < 1` → `n < 0` — 0 passes the button and comes back a 400 the operator
    // has to read to learn what this line already knew.
    expect(capProblem('12')).toBe('');
    expect(capProblem(' 8 ')).toBe('');
    expect(capProblem('-3')).toBe('the limit is a whole number, 1 or more');
    expect(capProblem('1.5')).toBe('the limit is a whole number, 1 or more');
    expect(capProblem('lots')).toBe('the limit is a whole number, 1 or more');
  });
});
