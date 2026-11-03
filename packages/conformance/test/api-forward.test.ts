/**
 * `/api/forward` — where this host forwards deploys and unserved visitors, over HTTP.
 *
 * The Go tests prove the establishing check against a second in-process server. What this file pins
 * from outside the binary is the wire shape: unset reads as JSON `null`, and a PUT whose check fails
 * is a 400 with an `error` and saves nothing.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer, type Booted } from '../harness/server.ts';
import { ALWAYS_OK, dockerShim } from '../harness/docker-shim.ts';

const j = (s: Booted, method: string, path: string, body?: unknown) =>
  fetch(`${s.base}${path}`, { method, headers: s.H, body: body === undefined ? undefined : JSON.stringify(body) });

describe('forwarding', () => {
  test('unset reads null; an unreachable target is refused and nothing is saved', async () => {
    // negative control: save the row before the check in forwardPut — the second GET is the row,
    // not null. Answer `{}` for an unset row — the first GET fails.
    const docker = dockerShim(ALWAYS_OK);
    const s = await bootServer({ tag: 'forward', pathPrefix: docker.dir });
    try {
      const before = await j(s, 'GET', '/api/forward');
      expect(before.status).toBe(200);
      expect(await before.json()).toBeNull();

      const put = await j(s, 'PUT', '/api/forward', { to: 'https://127.0.0.1:1', token: 'pstack_pat_conformance' });
      expect(put.status).toBe(400);
      const err = ((await put.json()) as { error: string }).error;
      expect(err).toContain('cannot reach 127.0.0.1:1');
      expect(err).not.toContain('pstack_pat_conformance');

      expect(await (await j(s, 'GET', '/api/forward')).json()).toBeNull();
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 30_000);
});
