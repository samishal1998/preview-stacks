import { expect, test } from 'bun:test';
import type { Job } from '@samyx/preview-stacks-client';
import { atLeast, daysLeft, domainProblem, mem, rangeProblem, storageBadge, supersededBy } from '../src/lib/control';

test('roles', () => {
  expect(atLeast(true, undefined, 'admin')).toBe(true);
  expect(atLeast(false, 'maintainer', 'admin')).toBe(false);
  expect(atLeast(false, 'admin', 'maintainer')).toBe(true);
  expect(atLeast(false, 'wizard', 'viewer')).toBe(false);
});
test('mem', () => {
  expect(mem(null)).toBe('unlimited');
  expect(mem(268435456)).toBe('256 MiB');
  expect(mem(2 * 1024 ** 3)).toBe('2.0 GiB');
});
test('days and badge', () => {
  expect(daysLeft(10 * 86_400_000 + 5, 0)).toBe(10);
  expect(storageBadge(null)).toBe('Filesystem');
  expect(storageBadge({ cutover: '2099-01-01' })).toBe('S3 from 2099-01-01');
  expect(storageBadge({ cutover: '2000-01-01' })).toBe('S3');
});
test('domain', () => {
  expect(domainProblem(' ', [], 'a.com')).not.toBe('');
  expect(domainProblem('https://x.com', [], 'a.com')).not.toBe('');
  expect(domainProblem('A.com', [], 'a.com')).toBe('Already listed.');
  expect(domainProblem('b.com', ['c.com'], 'a.com')).toBe('');
});
test('range', () => {
  expect(rangeProblem(null, { min: 1, max: 5 })).not.toBe('');
  expect(rangeProblem(6, { min: 1, max: 5 })).not.toBe('');
  expect(rangeProblem(3, { min: 1, max: 5 })).toBe('');
});
test('supersededBy', () => {
  const j = (id: string, stack: string) => ({ id, stack }) as Job;
  expect(supersededBy(j('a', 's'), [j('c', 'x'), j('b', 's'), j('a', 's')])?.id).toBe('b');
  expect(supersededBy(j('z', 's'), [])).toBeNull();
});

test('why names the failure', async () => {
  const { why } = await import('../src/api-control');
  const r = (status: number, error?: string) => ({ ok: false, status, body: error ? { error } : {} }) as never;
  expect(why(r(0), 'Save')).toBe('The server did not answer.');
  expect(why(r(401), 'Save')).toBe('Save: sign in again.');
  expect(why(r(403), 'Save')).toBe('Save: not allowed for your role.');
  expect(why(r(500, 'boom'), 'Save')).toBe('Save: boom');
  expect(why(r(502), 'Save')).toBe('Save: HTTP 502');
});
