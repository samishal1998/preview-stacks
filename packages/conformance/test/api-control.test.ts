/**
 * `GET /api/control/runtime` and `POST /api/control/restart` — the control stack, visible.
 *
 * The view's reason to exist is the two fields nothing surfaced before: restart count and
 * OOM-killed. The action's reason to be TESTED is its refusal: the API must never restart the
 * container answering the request, whoever asks — that rule lives in one place and this file is
 * what keeps it there.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer, type Booted } from '../harness/server.ts';
import { arm, dockerShim } from '../harness/docker-shim.ts';

const PS = 'ps -aq --filter label=com.docker.compose.project=pstack-control';
const INSPECT = JSON.stringify([
  {
    Id: 't1',
    Name: '/pstack-control-traefik-1',
    RestartCount: 3,
    Config: { Image: 'traefik:v3.6.1', Labels: { 'com.docker.compose.service': 'traefik' } },
    State: { Status: 'running', StartedAt: '2026-08-27T12:00:15Z', OOMKilled: true },
    HostConfig: { Memory: 268435456 },
  },
  {
    Id: 'p1',
    Name: '/pstack-control-pstack-1',
    RestartCount: 0,
    Config: { Image: 'pstack:local', Labels: { 'com.docker.compose.service': 'pstack' } },
    State: { Status: 'running', StartedAt: '2026-08-27T01:00:00Z' },
    HostConfig: { Memory: 0 },
  },
]);

const controlArms = [arm(PS, 't1\np1'), arm('inspect t1 p1', INSPECT)].join('\n');

const j = (s: Booted, method: string, path: string, body?: unknown) =>
  fetch(`${s.base}${path}`, { method, headers: s.H, body: body === undefined ? undefined : JSON.stringify(body) });

describe('the control stack, visible', () => {
  test('runtime surfaces restart counts, OOM kills and memory limits', async () => {
    // negative control: drop OOMKilled/HostConfig from inspect's rawInspect — both fields zero
    // out and the page can no longer answer "why did traefik restart at noon".
    const docker = dockerShim(controlArms);
    const s = await bootServer({ tag: 'ctl', pathPrefix: docker.dir });
    try {
      const r = await j(s, 'GET', '/api/control/runtime');
      expect(r.status).toBe(200);
      const v = (await r.json()) as { reachable: boolean; containers: Record<string, unknown>[] };
      expect(v.reachable).toBe(true);
      // Sorted by service — pstack before traefik — not by docker's order.
      expect(v.containers.map((c) => c.service)).toEqual(['pstack', 'traefik']);
      const traefik = v.containers[1]!;
      expect(traefik.restartCount).toBe(3);
      expect(traefik.oomKilled).toBe(true);
      expect(traefik.memLimitBytes).toBe(268435456);
      expect(v.containers[0]!.oomKilled).toBe(false);
      // Docker reports Memory:0 for "no limit"; the wire carries null — never a zero to special-case.
      expect(v.containers[0]!.memLimitBytes).toBeNull();
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);

  test('restart targets the inspected container, and pstack itself is always refused', async () => {
    // negative control: drop the self-refusal in inspect.RestartControlService — the API restarts
    // its own container, killing this very request (and any running job) in flight.
    const docker = dockerShim(controlArms, { record: true });
    const s = await bootServer({ tag: 'ctl-restart', pathPrefix: docker.dir });
    try {
      const ok = await j(s, 'POST', '/api/control/restart', { service: 'traefik' });
      expect(ok.status).toBe(200);
      expect(((await ok.json()) as { container: string }).container).toBe('pstack-control-traefik-1');
      expect(docker.calls()).toContain('restart t1');

      const before = docker.calls().filter((c) => c.startsWith('restart')).length;
      const self = await j(s, 'POST', '/api/control/restart', { service: 'pstack' });
      expect(self.status).toBe(400);
      expect(((await self.json()) as { error: string }).error).toContain('refusing to restart "pstack"');
      // The refusal happens before docker is asked anything.
      expect(docker.calls().filter((c) => c.startsWith('restart')).length).toBe(before);

      expect((await j(s, 'POST', '/api/control/restart', { service: 'nothing' })).status).toBe(404);
      expect((await j(s, 'POST', '/api/control/restart', {})).status).toBe(400);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);

  test('docker not answering is "unknown", never "empty" — and the restart says so with a 503', async () => {
    // negative control: collapse !ok to an empty listing in ControlRuntime — a live control stack
    // reports as torn down, and the restart's 503 becomes a lying 404.
    const docker = dockerShim(arm(PS, '', 1));
    const s = await bootServer({ tag: 'ctl-down', pathPrefix: docker.dir });
    try {
      const r = await j(s, 'GET', '/api/control/runtime');
      expect(r.status).toBe(200);
      const v = (await r.json()) as { reachable: boolean; containers: unknown[] };
      expect(v.reachable).toBe(false);
      expect(v.containers).toEqual([]);
      expect((await j(s, 'POST', '/api/control/restart', { service: 'traefik' })).status).toBe(503);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);
});
