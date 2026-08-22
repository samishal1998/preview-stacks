/**
 * The harness itself: a server comes up through the real CLI, the null server is reachable, and a
 * docker shim on the child's PATH is what the API talks to.
 */
import { describe, expect, test } from 'bun:test';
import { IMPL, NO_CLI } from '../harness/impl.ts';
import { bootServer } from '../harness/server.ts';
import { runCli } from '../harness/cli.ts';
import { arm, dockerShim } from '../harness/docker-shim.ts';

describe('harness', () => {
  test('boots a server and reaches /api/health with the token it was given', async () => {
    const s = await bootServer({ tag: 'h' });
    try {
      const h = (await (await fetch(`${s.base}/api/health`)).json()) as { ok: boolean; authEnforced?: boolean; version?: string };
      expect(h.ok).toBe(true);
      // The null server says `{}` — ok is undefined there, which is the whole point of vacuity.
      if (IMPL !== 'null') {
        expect(h.authEnforced).toBe(true);
        expect(typeof h.version).toBe('string');
        const anon = await fetch(`${s.base}/api/deployments`);
        expect(anon.status).toBe(401);
        const authed = await fetch(`${s.base}/api/deployments`, { headers: s.H });
        expect(authed.status).toBe(200);
      }
    } finally {
      await s.stop();
    }
  });

  test.skipIf(NO_CLI)('runs the CLI from packages/pstack so examples/ resolve', async () => {
    const r = await runCli(['--version']);
    expect(r.code).toBe(0);
    expect(r.stdout.trim()).toMatch(/^\d+\.\d+\.\d+/);
  });

  test.skipIf(NO_CLI)('a docker shim on the child PATH answers the API', async () => {
    const shim = dockerShim(
      [arm('compose ls --all --format json', '[{"Name":"shimmed","Status":"running(1)"}]')].join('\n'),
      { record: true },
    );
    const s = await bootServer({ tag: 'h2', pathPrefix: shim.dir });
    try {
      const put = await fetch(`${s.base}/api/deployments/shimmed`, {
        method: 'PUT',
        headers: s.H,
        body: JSON.stringify({ spec: 'version: 1\nstack: shimmed\naxes:\n  - name: a\n    up: "true"\n' }),
      });
      expect(put.status).toBe(201);
      const list = (await (await fetch(`${s.base}/api/deployments`, { headers: s.H })).json()) as { deployments: Array<{ id: string; running: boolean | null }> };
      expect(list.deployments.find((d) => d.id === 'shimmed')?.running).toBe(true);
      expect(shim.calls().some((c) => c.startsWith('compose ls'))).toBe(true);
    } finally {
      await s.stop();
      shim.remove();
    }
  });
});
