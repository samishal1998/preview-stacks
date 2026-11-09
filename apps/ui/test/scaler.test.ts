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
