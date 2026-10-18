import { expect, test } from 'bun:test';
import type { DeploymentRow } from '@samyx/preview-stacks-client';
import { chosen, sendable, skipReason } from '../src/bulk';

const d = (o: Partial<DeploymentRow>): DeploymentRow => ({ id: 'a', kind: 'isolated', stack: 's', busy: false, running: true, orchestrator: 'compose', ...o });

test('needs variables blocks every action', () => {
  expect(skipReason(d({ unresolved: 'PR' }), 'up')).toBe('needs variables');
  expect(skipReason(d({ stack: null }), 'verify')).toBe('needs variables');
});
test('shared is never torn down or slept in bulk', () => {
  expect(skipReason(d({ kind: 'shared' }), 'down')).toContain('shared');
  expect(skipReason(d({ kind: 'shared' }), 'down-forget')).toContain('shared');
  expect(skipReason(d({ kind: 'shared' }), 'sleep')).toContain('do not sleep');
  expect(skipReason(d({ kind: 'shared' }), 'up')).toBe('');
});
test('sleep and wake only where they mean something', () => {
  const asleep = { since: 1, reason: 'idle', hosts: [], rules: [] };
  expect(skipReason(d({ orchestrator: null }), 'sleep')).toBe('no compose section');
  expect(skipReason(d({ asleep }), 'sleep')).toBe('already asleep');
  expect(skipReason(d({}), 'wake')).toBe('not asleep');
  expect(skipReason(d({ asleep }), 'wake')).toBe('');
});
test('a hidden row is never chosen, and sendable counts what would go', () => {
  const rows = [d({ id: 'a' }), d({ id: 'b', kind: 'shared' })];
  expect(chosen(rows, ['a', 'gone']).map((x) => x.id)).toEqual(['a']);
  expect(sendable(rows, 'down')).toBe(1);
  expect(sendable(rows, 'up')).toBe(2);
});
