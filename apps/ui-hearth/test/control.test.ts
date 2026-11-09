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

test('scaler card shows while on, unknown, or still owning workers', async () => {
  // negative control: showing on `enabled !== false` alone hides a turned-off scaler that still owns workers.
  const { scalerShown } = await import('../src/lib/control');
  const v = (enabled: boolean | null, workers: number) =>
    ({ enabled, report: workers ? { workers: Array(workers).fill({}) } : null }) as never;
  expect(scalerShown(v(true, 0))).toBe(true);
  expect(scalerShown(v(null, 0))).toBe(true);
  expect(scalerShown(v(false, 0))).toBe(false);
  expect(scalerShown(v(false, 2))).toBe(true);
});

test('scaler lock needs a lock record and an owned worker', async () => {
  // negative control: reading `report.locked` alone keeps the preset locked after the last worker is gone.
  const { scalerLocked } = await import('../src/lib/control');
  const locked = { preset: 'hetzner', filesHash: 'h', shared: {} };
  expect(scalerLocked(null)).toBe(false);
  expect(scalerLocked({ locked, workers: [] } as never)).toBe(false);
  expect(scalerLocked({ locked: null, workers: [{}] } as never)).toBe(false);
  expect(scalerLocked({ locked, workers: [{}] } as never)).toBe(true);
});

test('scaler save body', async () => {
  // negative control: sending the draft's vars whole (or dropping `dismissed`) makes a preset switch 400 and a save un-dismiss every orphan.
  const { openOrphans, scalerBody } = await import('../src/lib/control');
  const s = { preset: 'hetzner', vars: { server_type: 'cx22', region: '', size: 's-1' }, min: 0, max: 3, cooldownMin: 5, graceMin: 15, dismissed: ['gone', 'pstack-aaaaaa'] };
  const p = { perWorker: ['server_type'], shared: ['region', 'location'], distro: 'ubuntu', hash: 'h', lockFile: true };
  const body = scalerBody(s, p, ['pstack-aaaaaa', 'pstack-bbbbbb']);
  expect(body.vars).toEqual({ server_type: 'cx22' });
  expect(body.dismissed).toEqual(['pstack-aaaaaa']);
  expect('credentials' in body).toBe(false);
  expect(scalerBody(s, p, [], { A: '', B: 'v', '': 'x' }).credentials).toEqual({ B: 'v' });
  // negative control: building a body without the preset sends vars {}, and PUT wipes every stored var.
  expect(scalerBody(s, undefined, [])).toBeNull();
  const v = { settings: s, report: { orphans: ['pstack-aaaaaa', 'pstack-bbbbbb'] } } as never;
  expect(openOrphans(v)).toEqual(['pstack-bbbbbb']);
});
