/**
 * The routing-file and registry-credential routes, over HTTP. Ported from the two nested
 * 'over HTTP' describes in packages/pstack/test/stack.test.ts ('Traefik dynamic config' and
 * 'private registry credentials'); the in-process store tests around them are not black-box and
 * stay there.
 */
import { describe, expect, test } from 'bun:test';
import { mkdirSync, readdirSync, rmSync } from 'node:fs';
import { bootServer, tmpd } from '../harness/server.ts';

describe("Traefik dynamic config — one bad write must not take down other people's routes", () => {
  /*
   * Traefik's documented behaviour: an unparseable file in the watched directory is a parse error for
   * the DIRECTORY, and the rest of it can be discarded with it. So the failure mode is not "my new
   * middleware is broken", it is routes elsewhere vanishing. Everything below defends that.
   */
  const VALID = 'http:\n  middlewares:\n    dashboard-auth:\n      basicAuth:\n        users:\n          - "admin:$apr1$x"\n';

  describe('over HTTP', () => {
    const boot = async () => {
      const routingDir = tmpd('routing-dynamic');
      mkdirSync(routingDir, { recursive: true });
      const s = await bootServer({ tag: 'routing', routingDir });
      return { s, base: s.base, routingDir, stop: async () => {
        await s.stop();
        rmSync(routingDir, { recursive: true, force: true });
      } };
    };

    test('everything is behind the gate; content round-trips for an authed caller', async () => {
      const { s, base, stop } = await boot();
      const authz = { authorization: s.H.authorization! };
      try {
        const put = await fetch(`${base}/api/routing/auth.yml`, {
          method: 'PUT',
          headers: { ...authz, 'content-type': 'application/json' },
          body: JSON.stringify({ content: VALID }),
        });
        expect(put.status).toBe(201);

        // 0.10.0: the file LIST needs auth too — everything does.
        expect((await fetch(`${base}/api/routing`)).status).toBe(401);
        const unauthedRead = await fetch(`${base}/api/routing/auth.yml`);
        expect(unauthedRead.status).toBe(401);
        expect(await unauthedRead.text()).not.toContain('apr1');

        const list = (await (await fetch(`${base}/api/routing`, { headers: authz })).json()) as {
          files: Array<{ name: string }>;
          writable: boolean;
        };
        expect(list.files.map((f) => f.name)).toEqual(['auth.yml']);
        expect(list.writable).toBe(true);

        const authedRead = (await (
          await fetch(`${base}/api/routing/auth.yml`, { headers: authz })
        ).json()) as { content: string };
        expect(authedRead.content).toBe(VALID);
      } finally {
        await stop();
      }
    });

    test('a write without a token is refused, and changes nothing', async () => {
      const { base, routingDir, stop } = await boot();
      try {
        const r = await fetch(`${base}/api/routing/nope.yml`, {
          method: 'PUT',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ content: VALID }),
        });
        expect(r.status).toBe(401);
        expect(readdirSync(routingDir).filter((f) => f.endsWith('.yml'))).toEqual([]);
      } finally {
        await stop();
      }
    });

    test('a rejected file answers 400 with the reason, not 500', async () => {
      const { s, base, stop } = await boot();
      try {
        const r = await fetch(`${base}/api/routing/bad.yml`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ content: 'htttp: {}' }),
        });
        expect(r.status).toBe(400);
        expect(((await r.json()) as { error: string }).error).toMatch(/unknown top-level/);
      } finally {
        await stop();
      }
    });
  });
});

describe('private registry credentials — the client authenticates the pull, not the daemon', () => {
  const PASSWORD = 'ghp-super-secret-registry-token';
  // The canonical key `docker login` writes for Docker Hub — what every alias must collapse to,
  // because docker looks `nginx:alpine` up under exactly this and nothing else.
  const DOCKER_HUB_KEY = 'https://index.docker.io/v1/';

  describe('over HTTP', () => {
    const boot = async () => {
      const registryDir = tmpd('reg-docker');
      const s = await bootServer({ tag: 'reg', registryDir });
      return { s, base: s.base, registryDir, stop: async () => {
        await s.stop();
        rmSync(registryDir, { recursive: true, force: true });
      } };
    };

    test('stored and listed with auth; the secret is never in any response', async () => {
      const { s, base, stop } = await boot();
      try {
        const put = await fetch(`${base}/api/registries/ghcr.io`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ username: 'sami', password: PASSWORD }),
        });
        expect(put.status).toBe(200);
        // The normalized key is echoed, since it can differ from what was sent.
        expect(((await put.json()) as { registry: string }).registry).toBe('ghcr.io');

        // 0.10.0: the list needs auth too. The unauthenticated refusal must carry nothing.
        const unauthed = await fetch(`${base}/api/registries`);
        expect(unauthed.status).toBe(401);
        expect(await unauthed.text()).not.toContain('ghcr');

        const raw = await (
          await fetch(`${base}/api/registries`, { headers: { authorization: s.H.authorization! } })
        ).text();
        expect(raw).not.toContain(PASSWORD);
        expect(raw).not.toContain(Buffer.from(`sami:${PASSWORD}`).toString('base64'));
        const body = JSON.parse(raw) as {
          entries: Array<{ registry: string; username: string; viaHelper: boolean }>;
        };
        expect(body.entries).toEqual([{ registry: 'ghcr.io', username: 'sami', viaHelper: false }]);
      } finally {
        await stop();
      }
    });

    test('an unauthenticated write is refused and stores nothing', async () => {
      const { base, registryDir, stop } = await boot();
      try {
        const r = await fetch(`${base}/api/registries/ghcr.io`, {
          method: 'PUT',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ username: 'sami', password: PASSWORD }),
        });
        expect(r.status).toBe(401);
        expect(await Bun.file(`${registryDir}/config.json`).exists()).toBe(false);
      } finally {
        await stop();
      }
    });

    test('docker.io is stored under the canonical key, and can be deleted by alias', async () => {
      const { s, base, stop } = await boot();
      try {
        const put = await fetch(`${base}/api/registries/docker.io`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ username: 'sami', password: 'p' }),
        });
        expect(((await put.json()) as { registry: string }).registry).toBe(DOCKER_HUB_KEY);

        const del = await fetch(`${base}/api/registries/${encodeURIComponent('index.docker.io')}`, {
          method: 'DELETE',
          headers: { authorization: s.H.authorization! },
        });
        expect(del.status).toBe(200);
      } finally {
        await stop();
      }
    });

    test('deleting something that is not stored is a 404, not a silent success', async () => {
      const { s, base, stop } = await boot();
      try {
        const r = await fetch(`${base}/api/registries/ghcr.io`, {
          method: 'DELETE',
          headers: { authorization: s.H.authorization! },
        });
        expect(r.status).toBe(404);
      } finally {
        await stop();
      }
    });

    test('a malformed host answers 400 with the reason', async () => {
      const { s, base, stop } = await boot();
      try {
        const r = await fetch(`${base}/api/registries/${encodeURIComponent('not a host')}`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ username: 'a', password: 'b' }),
        });
        expect(r.status).toBe(400);
        expect(((await r.json()) as { error: string }).error).toMatch(/does not look like a registry/);
      } finally {
        await stop();
      }
    });
  });
});
