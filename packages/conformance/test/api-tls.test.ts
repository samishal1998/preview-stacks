/**
 * `/api/tls` — the bring-your-own wildcard (`dns-persist-01`), over HTTP.
 *
 * The mode is DERIVED from artifacts, never stored: a wildcard pair lands in Traefik's watched
 * directory and the mode IS its presence. What only a real process shows: the round trip through
 * the routing directory, the label rule flipping on the NEXT deploy (read back from the generated
 * compose file, byte-level), and the redeploy loop skipping what must not be woken.
 */
import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { bootServer, tmpd, type Booted } from '../harness/server.ts';
import { ALWAYS_OK, dockerShim } from '../harness/docker-shim.ts';

const CERT = readFileSync(join(import.meta.dir, '../harness/tls-wildcard.crt.pem'), 'utf8');
const KEY = readFileSync(join(import.meta.dir, '../harness/tls-wildcard.key.pem'), 'utf8');

const SPEC = 'version: 1\nstack: pr-t\nenv:\n  PREVIEW_DOMAIN: preview.example.com\ncompose: {file: compose.yml, profiles: []}\naxes: []\n';
const COMPOSE = 'services:\n  app:\n    image: x\n    labels:\n      - pstack.routing.port=80\n';

async function boot(tag: string) {
  const routingDir = tmpd('tls-dynamic');
  mkdirSync(routingDir, { recursive: true });
  const docker = dockerShim(ALWAYS_OK);
  const s = await bootServer({ tag, routingDir, domain: 'preview.example.com', pathPrefix: docker.dir });
  return { s, routingDir, stop: async () => { await s.stop(); docker.remove(); } };
}

const j = (s: Booted, method: string, path: string, body?: unknown) =>
  fetch(`${s.base}${path}`, { method, headers: s.H, body: body === undefined ? undefined : JSON.stringify(body) });

/** Deploy and wait for the up job to settle; returns the generated compose file's bytes. */
async function deployAndRead(s: Booted, id: string): Promise<string> {
  expect((await j(s, 'PUT', `/api/deployments/${id}`, { spec: SPEC.replaceAll('pr-t', id), compose: COMPOSE })).status).toBeLessThan(300);
  expect((await j(s, 'POST', `/api/deployments/${id}/up`, {})).status).toBeLessThan(300);
  const deadline = Date.now() + 15_000;
  for (;;) {
    const jobs = ((await (await j(s, 'GET', '/api/jobs')).json()) as { jobs: Array<{ stack: string; state: string }> }).jobs;
    const done = jobs.find((x) => x.stack === id && x.state !== 'running' && x.state !== 'queued');
    if (done) {
      expect(done.state).toBe('ok');
      break;
    }
    if (Date.now() > deadline) throw new Error('up never settled');
    await new Promise((r) => setTimeout(r, 50));
  }
  return readFileSync(join(s.dataDir, 'deployments', id, 'compose.generated.yml'), 'utf8');
}

describe('the certificate mode over HTTP', () => {
  test('storing a wildcard flips the mode, the label rule, and back', async () => {
    // negative control: keep stamping certresolver regardless of the stored wildcard — the second
    // deploy below carries it, and on a live host every preview orders its own certificate past
    // the wildcard, straight into the 50-per-week limit.
    const { s, stop } = await boot('tls');
    try {
      // Before: no wildcard, mode is whatever traefik's argv says — the shim has no traefik.
      const before = (await (await j(s, 'GET', '/api/tls')).json()) as { mode: string; wildcard: unknown };
      expect(before.mode).toBe('unknown');
      expect(before.wildcard).toBeNull();
      // A deploy in this state carries the resolver (unknown errs toward it — http01 safety).
      expect(await deployAndRead(s, 'pr-before')).toContain('tls.certresolver=le');

      const put = await j(s, 'PUT', '/api/tls/wildcard', { cert: CERT, key: KEY });
      expect(put.status).toBe(200);
      const stored = (await put.json()) as { wildcard: { domains: string[] } };
      expect(stored.wildcard.domains).toContain('*.preview.example.com');

      const after = (await (await j(s, 'GET', '/api/tls')).json()) as { mode: string; wildcard: { domains: string[] } };
      expect(after.mode).toBe('dns-persist-01');
      // The pointer file is an ordinary routing file — visible where every dynamic file is.
      const files = (await (await j(s, 'GET', '/api/routing')).json()) as { files: Array<{ name: string }> };
      expect(files.files.map((f) => f.name)).toContain('tls-wildcard.yml');

      // THE POINT: the next deploy drops the resolver — the wildcard covers it by SNI.
      expect(await deployAndRead(s, 'pr-after')).not.toContain('certresolver');

      // Leaving the mode restores the resolver on the next deploy.
      expect((await j(s, 'DELETE', '/api/tls/wildcard')).status).toBe(200);
      expect((await j(s, 'DELETE', '/api/tls/wildcard')).status).toBe(404);
      expect(await deployAndRead(s, 'pr-restored')).toContain('tls.certresolver=le');
    } finally {
      await stop();
    }
  }, 60_000);

  test('a broken pair is refused with the reason, and the key never comes back out', async () => {
    // negative control: skip tls.X509KeyPair — the swap below stores, and every preview serves a
    // handshake failure that only traefik's own logs explain.
    const { s, stop } = await boot('tls-refuse');
    try {
      const swapped = await j(s, 'PUT', '/api/tls/wildcard', { cert: KEY, key: CERT });
      expect(swapped.status).toBe(400);
      expect((await j(s, 'PUT', '/api/tls/wildcard', { cert: CERT })).status).toBe(400);

      expect((await j(s, 'PUT', '/api/tls/wildcard', { cert: CERT, key: KEY })).status).toBe(200);
      // Invariant 15: nothing returns the key. The status shows public facts only.
      const status = JSON.stringify(await (await j(s, 'GET', '/api/tls')).json());
      expect(status).not.toContain('PRIVATE KEY');

      // THE ADMIN GATE IS ON THE ARTIFACT, NOT ONLY ON THE ROUTE. The generic routing API is a
      // MAINTAINER's, and the pointer file's presence IS the mode — so writing or deleting that one
      // name through /api/routing would walk around the admin gate above (and, deleting, orphan the
      // key pair). Refused for everyone, root included: this caller holds the machine token.
      const hijack = await j(s, 'PUT', '/api/routing/tls-wildcard.yml', { content: 'tls:\n  certificates: []\n' });
      expect(hijack.status).toBe(400);
      expect(((await hijack.json()) as { error: string }).error).toContain('managed by pstack');
      expect((await j(s, 'DELETE', '/api/routing/tls-wildcard.yml')).status).toBe(400);
      // And the mode is untouched by the attempt.
      expect(((await (await j(s, 'GET', '/api/tls')).json()) as { mode: string }).mode).toBe('dns-persist-01');
    } finally {
      await stop();
    }
  }, 30_000);

  test('the redeploy loop redeploys the awake and skips the asleep', async () => {
    // negative control: drop the Sleep check in tlsRedeploy — the sleeping stack below is redeployed,
    // which IS a wake nobody asked for, and its hostname stops answering the waking page mid-flight.
    const { s, stop } = await boot('tls-redeploy');
    try {
      await deployAndRead(s, 'pr-awake');
      await deployAndRead(s, 'pr-napper');
      expect((await j(s, 'POST', '/api/deployments/pr-napper/sleep', {})).status).toBeLessThan(300);
      const deadline = Date.now() + 15_000;
      for (;;) {
        const jobs = ((await (await j(s, 'GET', '/api/jobs')).json()) as { jobs: Array<{ stack: string; action: string; state: string }> }).jobs;
        if (jobs.some((x) => x.stack === 'pr-napper' && x.action === 'sleep' && x.state === 'ok')) break;
        if (Date.now() > deadline) throw new Error('sleep never settled');
        await new Promise((r) => setTimeout(r, 50));
      }

      const r = (await (await j(s, 'POST', '/api/tls/redeploy')).json()) as {
        started: Array<{ id: string; job: string }>;
        skipped: Array<{ id: string; reason: string }>;
      };
      expect(r.started.map((x) => x.id)).toEqual(['pr-awake']);
      expect(r.skipped.some((x) => x.id === 'pr-napper' && x.reason.includes('asleep'))).toBe(true);
    } finally {
      await stop();
    }
  }, 60_000);
});
