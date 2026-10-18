import { expect, test } from 'bun:test';
import { joinPath, joinProblem, removable } from '../src/lib/swarm';

test('swarm rules', () => {
  expect(joinPath('command', 'x')).toBe('/api/swarm/join?format=command');
  expect(joinPath('cloud-config', 'ubuntu')).toContain('&distro=ubuntu');
  expect(removable({ state: 'down', availability: 'drain' })).toBe(true);
  expect(removable({ state: 'ready', availability: 'drain' })).toBe(false);
  expect(removable({ state: 'down', availability: 'drain', self: true })).toBe(false);
  expect(joinProblem(409, '')).toBe('Not a swarm manager.');
  expect(joinProblem(500, 'boom')).toBe('boom');
});
