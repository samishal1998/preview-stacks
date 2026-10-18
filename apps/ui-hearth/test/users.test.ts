import { expect, test } from 'bun:test';
import { can, canCreateUser, isLastAdmin, pickerOptions, problem, rank } from '../src/lib/users';

test('rank fails closed on unknown roles', () => {
  expect(rank('admin')).toBe(4);
  expect(rank('wizard')).toBe(0);
  expect(rank(undefined)).toBe(0);
});

test('can: root passes, unknown role never does', () => {
  expect(can('admin', { root: true })).toBe(true);
  expect(can('developer', { root: false, role: 'maintainer' })).toBe(true);
  expect(can('admin', { root: false, role: 'maintainer' })).toBe(false);
  expect(can('viewer', { root: false, role: 'wizard' })).toBe(false);
});

test('last admin counts account rows', () => {
  const us = [{ role: 'admin' }, { role: 'viewer' }];
  expect(isLastAdmin(us[0]!, us)).toBe(true);
  expect(isLastAdmin(us[1]!, us)).toBe(false);
  expect(isLastAdmin(us[0]!, [...us, { role: 'admin' }])).toBe(false);
});

test('create needs a username and 8+ characters', () => {
  expect(canCreateUser({ username: ' ', password: '12345678' })).toBe(false);
  expect(canCreateUser({ username: 'a', password: '1234567' })).toBe(false);
  expect(canCreateUser({ username: 'a', password: '12345678' })).toBe(true);
});

test('an unknown role still appears in the picker', () => {
  expect(pickerOptions('wizard')[0]!.value).toBe('wizard');
  expect(pickerOptions('admin').some((o) => o.value === 'wizard')).toBe(false);
});

test('problem carries the server words, and the outage', () => {
  expect(problem({ status: 403, body: { error: 'needs admin' } }, 'x')).toBe('Could not x. needs admin');
  expect(problem({ status: 0, body: {} }, 'x')).toContain('did not answer');
  expect(problem({ status: 500, body: {} }, 'x')).toContain('HTTP 500');
});
