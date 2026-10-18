import { expect, test } from 'bun:test';
import { PREFIX_MS, keyStep } from '../src/lib/shortcuts';

const k = (key: string, o: Partial<{ typing: boolean; mod: boolean }> = {}) => ({ key, typing: false, mod: false, ...o });

test('g then d goes to deployments', () => {
  const a = keyStep(0, k('g'), 1000);
  expect(a.pendingAt).toBe(1000);
  expect(keyStep(a.pendingAt, k('d'), 1100)).toEqual({ pendingAt: 0, go: '/deployments' });
});
test('an expired g is ignored, an unknown second key clears it', () => {
  expect(keyStep(1000, k('d'), 1000 + PREFIX_MS).go).toBeUndefined();
  expect(keyStep(1000, k('x'), 1100)).toEqual({ pendingAt: 0 });
});
test('? opens help; typing and modifiers do nothing', () => {
  expect(keyStep(0, k('?'), 1).help).toBe(true);
  expect(keyStep(0, k('?', { typing: true }), 1)).toEqual({ pendingAt: 0 });
  expect(keyStep(1, k('d', { mod: true }), 2)).toEqual({ pendingAt: 0 });
});
