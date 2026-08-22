/**
 * Host variables & secrets over HTTP — ported from packages/pstack/test/stack.test.ts
 * ('host variables & secrets — the GitHub model, scoped to one host'). The parseSpec test of that
 * describe is in-process and stays behind.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer } from '../harness/server.ts';

describe('host variables & secrets — the GitHub model, scoped to one host', () => {
  test('a variable reads back; a secret never does — not from any route', async () => {
    const s = await bootServer({ tag: 'hv' });
    const { base, H } = s;
    try {
      const put = (name: string, value: string, secret: boolean) =>
        fetch(`${base}/api/host-vars/${name}`, { method: 'PUT', headers: H, body: JSON.stringify({ value, secret }) });

      expect((await put('REGION', 'eu-central', false)).status).toBe(201);
      const sec = await put('DB_PASSWORD', 'hunter2-swordfish-42', true);
      expect(sec.status).toBe(201);
      // The 201 itself must not echo a secret back.
      expect(await sec.text()).not.toContain('hunter2-swordfish-42');

      const list = (await (await fetch(`${base}/api/host-vars`, { headers: H })).json()) as {
        entries: Array<{ name: string; value: string | null; secret: boolean }>;
      };
      expect(list.entries.find((e) => e.name === 'REGION')?.value).toBe('eu-central');
      const sv = list.entries.find((e) => e.name === 'DB_PASSWORD')!;
      expect(sv.secret).toBe(true);
      expect(sv.value).toBeNull();

      // secret → variable is an information-flow downgrade wearing an UPDATE's clothes.
      const downgrade = await put('DB_PASSWORD', 'anything', false);
      expect(downgrade.status).toBe(400);
      expect(((await downgrade.json()) as { error: string }).error).toMatch(/write-only|reveal/);
      // variable → secret only tightens, so it is allowed.
      expect((await put('REGION', 'eu-central', true)).status).toBe(200);
    } finally {
      await s.stop();
    }
  });

  test('a spec resolves ${vars.*} and ${secrets.*}; the log never contains the secret value', async () => {
    const s = await bootServer({ tag: 'hv' });
    const { base, H } = s;
    try {
      const put = (name: string, value: string, secret: boolean) =>
        fetch(`${base}/api/host-vars/${name}`, { method: 'PUT', headers: H, body: JSON.stringify({ value, secret }) });
      await put('GREETING', 'hello-from-host', false);
      await put('API_TOKEN', 'super-secret-token-value-9000', true);

      // The worst-case spec, aimed at the surface that actually records hook output: a FAILING
      // hook's stderr lands in the outcome's step message, which GET /api/jobs/:id serves forever.
      // (A succeeding hook's stdout is not recorded at all.) The greeting proves ${vars.*} resolved;
      // the failure line proves the secret's VALUE was scrubbed from the record.
      const spec = [
        'version: 1',
        'stack: hv-demo',
        'env:',
        '  TOKEN_FOR_APP: ${secrets.API_TOKEN}',
        'axes:',
        '  - name: a',
        '    up: echo greeting is ${vars.GREETING} and token is $TOKEN_FOR_APP 1>&2; exit 1',
        '    assert_gone: "true"',
      ].join('\n');
      const dep = await fetch(`${base}/api/deployments/hv`, {
        method: 'PUT',
        headers: H,
        body: JSON.stringify({ spec }),
      });
      expect(dep.status).toBe(201);

      await fetch(`${base}/api/deployments/hv/up`, { method: 'POST', headers: H, body: '{}' });
      let jobRaw = '';
      for (let i = 0; i < 40; i++) {
        const jl = (await (await fetch(`${base}/api/jobs`, { headers: H })).json()) as {
          jobs: Array<{ id: string; state: string }>;
        };
        const j = jl.jobs[0];
        if (j && j.state !== 'running') {
          jobRaw = await (await fetch(`${base}/api/jobs/${j.id}`, { headers: H })).text();
          break;
        }
        await Bun.sleep(100);
      }
      // The variable flowed through; the secret's VALUE did not survive into the record.
      expect(jobRaw).toContain('hello-from-host');
      expect(jobRaw).not.toContain('super-secret-token-value-9000');
      expect(jobRaw).toContain('••••••');
    } finally {
      await s.stop();
    }
  }, 20_000);
});
