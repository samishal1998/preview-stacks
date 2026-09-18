/**
 * Action labels, tested directly — there is no component harness in this app (useJobQueue.test.ts).
 *
 * Outside `src/` on purpose, like the other two: a `bun:test` import under `src/` fails `vue-tsc`.
 * Run with `cd apps/ui && bun test test/useFormat.test.ts`.
 */
import { expect, test } from 'bun:test';
import { actionLabel } from '../src/composables/useFormat';

test('a loki-apply job reads as Loki settings', () => {
  // negative control: delete the 'loki-apply' row from ACTION_LABELS — actionLabel falls back to the
  // raw verb, and the Jobs page lists a job called `loki-apply`.
  expect(actionLabel('loki-apply')).toBe('Loki settings');
});
