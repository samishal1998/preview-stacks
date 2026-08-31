/**
 * `/api/domains` — the hostnames this host answers on, over HTTP.
 *
 * The unit test proves the rendering; what only a real process shows is that the routers land in
 * Traefik's watched directory as ordinary dynamic config (so Traefik will actually load them), that
 * the list read back is DERIVED from that file rather than stored beside it, and that the file is
 * closed to the generic routing API that would otherwise let the two disagree.
 */
import { describe, expect, test } from 'bun:test';
import { mkdirSync } from 'node:fs';
import { bootServer, tmpd, type Booted } from '../harness/server.ts';
import { ALWAYS_OK, arm, dockerShim } from '../harness/docker-shim.ts';

async function boot(tag: string) {
  const routingDir = tmpd('domains-dynamic');
  mkdirSync(routingDir, { recursive: true });
  const docker = dockerShim(ALWAYS_OK);
  const s = await bootServer({ tag, routingDir, domain: 'preview.old.com', pathPrefix: docker.dir });
  return { s, stop: async () => { await s.stop(); docker.remove(); } };
}

const j = (s: Booted, method: string, path: string, body?: unknown) =>
  fetch(`${s.base}${path}`, { method, headers: s.H, body: body === undefined ? undefined : JSON.stringify(body) });

/** A docker whose control project contains the advanced-UI container. */
const ADVANCED_SHIM = [
  arm('ps -aq --filter label=com.docker.compose.project=pstack-control', 'u1'),
  arm(
    'inspect u1',
    JSON.stringify([
      {
        Id: 'u1',
        Name: '/pstack-control-advanced-ui-1',
        Config: { Image: 'pstack-ui:local', Labels: { 'com.docker.compose.service': 'advanced-ui' } },
        State: { Status: 'running' },
      },
    ]),
  ),
].join('\n');

describe('the hostnames this host answers on', () => {
  test('an added domain serves the console this host actually runs', async () => {
    // negative control: hardcode the API service for the console router — control.<added-domain>
    // serves the EMBEDDED basic UI while control.<primary> serves the SPA, and the operator sees
    // two different consoles depending on which hostname they typed. Reported from a real host.
    const routingDir = tmpd('domains-ui');
    mkdirSync(routingDir, { recursive: true });
    const docker = dockerShim(ADVANCED_SHIM);
    const s = await bootServer({ tag: 'domains-ui', routingDir, domain: 'preview.old.com', pathPrefix: docker.dir });
    try {
      expect(((await (await j(s, 'GET', '/api/domains')).json()) as { console: string }).console).toBe('advanced');
      expect((await j(s, 'PUT', '/api/domains', { domains: ['preview.new.com'] })).status).toBe(200);
      const body = await (await j(s, 'GET', '/api/routing/pstack-domains.yml')).text();
      expect(body).toContain('advanced-ui@docker');
      // The API and the waking page stay on the API container whatever the console serves.
      expect(body).toContain('pstack@docker');
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 30_000);

  test('adding a domain writes its routers and reads back derived from them', async () => {
    // negative control: store the list in a side channel instead of deriving it from the file —
    // the two disagree the first time anything touches the directory, and the API reports a domain
    // Traefik is not serving (or misses one it is).
    const { s, stop } = await boot('domains');
    try {
      const before = (await (await j(s, 'GET', '/api/domains')).json()) as { primary: string; domains: string[] };
      expect(before.primary).toBe('preview.old.com');
      expect(before.domains).toEqual([]);

      const put = await j(s, 'PUT', '/api/domains', { domains: ['preview.new.com', 'PREVIEW.new.com.'] });
      expect(put.status).toBe(200);
      // Case and a trailing dot are the same hostname — stored once.
      expect(((await put.json()) as { domains: string[] }).domains).toEqual(['preview.new.com']);

      expect(((await (await j(s, 'GET', '/api/domains')).json()) as { domains: string[] }).domains).toEqual(['preview.new.com']);

      // It is a real dynamic-config file, visible where every routing file is.
      const files = (await (await j(s, 'GET', '/api/routing')).json()) as { files: Array<{ name: string }> };
      expect(files.files.map((f) => f.name)).toContain('pstack-domains.yml');
      const body = await (await j(s, 'GET', '/api/routing/pstack-domains.yml')).text();
      for (const want of [
        'Host(`control.preview.new.com`)',
        'Host(`api.preview.new.com`)',
        'pstack@docker', // the provider suffix — an unqualified name resolves to pstack@file and is dropped
        'priority: 1', // the wake catch-all must lose to every preview's own router
      ]) {
        expect(body).toContain(want);
      }

      // Emptying takes the file away rather than leaving one Traefik still reads.
      expect((await j(s, 'PUT', '/api/domains', { domains: [] })).status).toBe(200);
      expect(((await (await j(s, 'GET', '/api/domains')).json()) as { domains: string[] }).domains).toEqual([]);
      // 400, not 404: that is what the routing API answers for a file that is not there.
      expect((await j(s, 'GET', '/api/routing/pstack-domains.yml')).status).toBe(400);
    } finally {
      await stop();
    }
  }, 30_000);

  test('the primary is refused, garbage is refused, and the file is not an operator file', async () => {
    // negative control: drop the primary check — a second control.<primary> router is rendered for
    // a hostname the container's labels already serve, and Traefik deletes BOTH on a name clash.
    // The console goes dark on the one domain that is supposed to always work.
    const { s, stop } = await boot('domains-refuse');
    try {
      const primary = await j(s, 'PUT', '/api/domains', { domains: ['preview.old.com'] });
      expect(primary.status).toBe(400);
      expect(((await primary.json()) as { error: string }).error).toContain('primary domain');

      expect((await j(s, 'PUT', '/api/domains', { domains: ['not a domain'] })).status).toBe(400);
      expect((await j(s, 'PUT', '/api/domains', { domains: 'preview.new.com' })).status).toBe(400);
      expect((await j(s, 'PUT', '/api/domains', {})).status).toBe(400);

      // Reserved: the generic routing API may not write the file whose routers ARE the list.
      const hijack = await j(s, 'PUT', '/api/routing/pstack-domains.yml', { content: 'http: {}\n' });
      expect(hijack.status).toBe(400);
      expect(((await hijack.json()) as { error: string }).error).toContain('managed by pstack');
    } finally {
      await stop();
    }
  }, 30_000);
});
