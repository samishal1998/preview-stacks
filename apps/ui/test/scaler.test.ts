/** Run with `cd apps/ui && bun test test/scaler.test.ts`. */
import { expect, test } from 'bun:test';
import type { ScalerSettings } from '../src/api/types';
import { scalerSavedKey } from '../src/composables/scaler';

const base: ScalerSettings = { preset: 'hetzner', vars: { location: 'fsn1' }, min: 0, max: 3, cooldownMin: 10, graceMin: 30, dismissed: [] };

test('a saved vars change moves the key', () => {
  expect(scalerSavedKey({ ...base, vars: { location: 'nbg1' } })).not.toBe(scalerSavedKey(base));
});

test('a dismissal alone does not', () => {
  expect(scalerSavedKey({ ...base, dismissed: ['w1'] })).toBe(scalerSavedKey(base));
});

test('scaler numbers refuse a blank, a fraction, a negative and min > max', async () => {
  // negative control: reading a blank box as Number('') = 0 lets a cleared Max save `max: 0` and drain every worker.
  const { scalerNumbersProblem } = await import('../src/composables/scaler');
  const ok = { min: 0, max: 3, cooldownMin: 5, graceMin: 15 };
  expect(scalerNumbersProblem(ok)).toBe('');
  expect(scalerNumbersProblem({ ...ok, max: '' })).not.toBe('');
  expect(scalerNumbersProblem({ ...ok, max: 1.5 })).not.toBe('');
  expect(scalerNumbersProblem({ ...ok, graceMin: -1 })).not.toBe('');
  expect(scalerNumbersProblem({ ...ok, min: 4 })).not.toBe('');
  expect(scalerNumbersProblem({ ...ok, max: 0, min: 0 })).toBe('');
});
