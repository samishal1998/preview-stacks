/**
 * `/api/scaler` — the worker scaler's settings, over HTTP.
 *
 * The Go tests prove the credential rules, the lock and the root-only routes. What this file pins
 * from outside the binary is the wire shape of an unset host — the defaults, no report, and
 * `enabled: false` when the control stack has no scaler container — and that a PUT naming an unknown
 * preset is a 400 that saves nothing.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer } from '../harness/server.ts';
import { ALWAYS_OK, dockerShim } from '../harness/docker-shim.ts';

describe('worker scaler', () => {
  test('unset reads the defaults; an unknown preset is refused and nothing is saved', async () => {
    // negative control: default max to 0 in store.GetScalerConfig — the first expectation fails.
    // Save before the preset check in scalerPut — the second GET reads preset "nope".
    const docker = dockerShim(ALWAYS_OK);
    const s = await bootServer({ tag: 'scaler', pathPrefix: docker.dir });
    try {
      const get = async () => {
        const res = await fetch(`${s.base}/api/scaler`, { headers: s.H });
        expect(res.status).toBe(200);
        return (await res.json()) as { settings: { preset: string; max: number }; credentials: object; report: unknown; enabled: boolean | null };
      };
      const before = await get();
      expect(before.settings.max).toBe(3);
      expect(before.settings.preset).toBe('');
      expect(before.credentials).toEqual({});
      expect(before.report).toBeNull();
      expect(before.enabled).toBe(false);

      const put = await fetch(`${s.base}/api/scaler`, { method: 'PUT', headers: s.H, body: JSON.stringify({ preset: 'nope' }) });
      expect(put.status).toBe(400);
      expect(((await put.json()) as { error: string }).error).toContain('nope');

      expect((await get()).settings.preset).toBe('');
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 30_000);
});
