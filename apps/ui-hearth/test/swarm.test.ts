import { expect, test } from 'bun:test';
import { hasReadyWorker, joinPath, joinProblem, removable, thisManager, workersOnly } from '../src/lib/swarm';

test('swarm rules', () => {
  expect(joinPath('command', 'x')).toBe('/api/swarm/join?format=command');
  expect(joinPath('cloud-config', 'ubuntu')).toContain('&distro=ubuntu');
  expect(removable({ state: 'down', availability: 'drain' })).toBe(true);
  expect(removable({ state: 'ready', availability: 'drain' })).toBe(false);
  expect(removable({ state: 'down', availability: 'drain', self: true })).toBe(false);
  expect(joinProblem(409, '')).toBe('Not a swarm manager.');
  expect(joinProblem(500, 'boom')).toBe('boom');
});

// negative control: drop the `!n.self` test — the drained manager counts as the worker that previews run on.
test('where previews run', () => {
  const mgr = { role: 'manager', status: 'ready', availability: 'active', self: true };
  const wrk = { role: 'worker', status: 'ready', availability: 'active' };
  expect(thisManager([wrk, mgr])).toBe(mgr);
  expect(thisManager([wrk])).toBeUndefined();
  expect(workersOnly([mgr, wrk])).toBe(false);
  expect(workersOnly([{ ...mgr, availability: 'drain' }, wrk])).toBe(true);
  expect(hasReadyWorker([mgr])).toBe(false);
  expect(hasReadyWorker([mgr, wrk])).toBe(true);
  expect(hasReadyWorker([mgr, { ...wrk, status: 'down' }])).toBe(false);
  expect(hasReadyWorker([mgr, { ...wrk, availability: 'drain' }])).toBe(false);
});
