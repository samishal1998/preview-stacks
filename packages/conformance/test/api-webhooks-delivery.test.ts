/**
 * Webhooks, black-box: the URL guard, delivery (signing, retry, the event catalogue actually being
 * emitted), registration, and the queue + redelivery. Ported from packages/pstack/test/stack.test.ts
 * ('webhooks — composable notifications' from 'the URL guard' on, and 'delivery queueing and
 * redelivery'). Where the originals DROVE the server with `events.emit`, a real trigger stands in:
 * a spec PUT emits `spec.stored`, a `down` on a lying axis emits `job.leaked`.
 *
 * The harness boots every server with PSTACK_NOTIFY_ALLOW_PRIVATE=1 (the receivers live on
 * 127.0.0.1), so the originals' env flips collapse to one explicit unset in the URL-guard test.
 */
import { describe, expect, test } from 'bun:test';
import { mkdirSync, rmSync } from 'node:fs';
import { bootServer, tmpd, until, type Booted } from '../harness/server.ts';
import { receiver, redirector } from '../harness/receiver.ts';

const register = (s: Booted, body: Record<string, unknown>) =>
  fetch(`${s.base}/api/notifiers`, { method: 'POST', headers: s.H, body: JSON.stringify(body) });

describe('webhooks — composable notifications', () => {
  describe('the URL guard', () => {
    test('loopback, link-local and private addresses are refused with 400 and a reason', async () => {
      const s = await bootServer({ tag: 'wh-guard', env: { PSTACK_NOTIFY_ALLOW_PRIVATE: undefined } });
      try {
        for (const url of [
          'http://127.0.0.1/hook',
          'http://localhost/hook',
          // Cloud metadata — instance credentials. The single worst destination.
          'http://169.254.169.254/latest/meta-data/',
          'http://10.1.2.3/hook',
          'http://192.168.1.5/hook',
          'http://172.16.0.9/hook',
          // IPv6 literals. `URL.hostname` KEEPS the brackets, so a check written for a bare address
          // never fires — every one of these was allowed through before the classifier replaced it.
          'http://[::1]/hook',
          'http://[fc00::1]/hook',
          'http://[fd12:3456::1]/hook',
          'http://[fe80::1]/hook',
          'http://[::ffff:127.0.0.1]/hook',
        ]) {
          const r = await register(s, { name: 'x', events: ['*'], config: { url } });
          expect(`${url} → ${r.status}`).toBe(`${url} → 400`);
          expect(((await r.json()) as { error: string }).error).toMatch(/loopback, link-local or private/);
        }
        // 172.32 is NOT in 172.16.0.0/12 — a naive `172.` prefix check would over-match it.
        const publicish = await register(s, {
          name: 'ok',
          events: ['*'],
          config: { url: 'http://172.32.0.1/hook' },
        });
        expect(publicish.status).toBe(201);

        // The other half of the same mistake, and the one that bites real operators: a prefix test
        // for the IPv6 ranges matches DNS NAMES that merely start with those letters. Firebase is a
        // plausible webhook destination and was being refused as "a private address".
        for (const url of [
          'https://fcm.googleapis.com/hook',
          'https://fd-cdn.example.com/hook',
          'https://fe80-notreally.com/hook',
          'https://localhost.mycompany.com/hook',
        ]) {
          const r = await register(s, { name: `n${url.length}`, events: ['*'], config: { url } });
          expect(`${url} → ${r.status}`).toBe(`${url} → 201`);
        }
      } finally {
        await s.stop();
      }
    });

    test('a prototype key is not a notifier type — 400, not a 500 from inherited junk', async () => {
      const s = await bootServer({ tag: 'wh-proto' });
      try {
        // `TYPES` was an object literal, so `TYPES['constructor']` was truthy and skipped the
        // unknown-type guard, then blew up on `.validate` as an unmapped 500.
        for (const type of ['constructor', '__proto__', 'toString', 'hasOwnProperty', 'nope']) {
          const r = await fetch(`${s.base}/api/notifiers`, {
            method: 'POST',
            headers: s.H,
            body: JSON.stringify({ type, name: 'x', events: ['job.failed'], config: {} }),
          });
          expect(`${type} → ${r.status}`).toBe(`${type} → 400`);
          expect(((await r.json()) as { error: string }).error).toMatch(/unknown notifier type/);
        }
      } finally {
        await s.stop();
      }
    });

    test('a non-http scheme is refused', async () => {
      const s = await bootServer({ tag: 'wh-scheme' });
      try {
        const r = await register(s, { name: 'x', events: ['*'], config: { url: 'file:///etc/passwd' } });
        expect(r.status).toBe(400);
        expect(((await r.json()) as { error: string }).error).toMatch(/only http and https/);
      } finally {
        await s.stop();
      }
    });

    test('a redirect is a FAILURE, not a hop to follow', async () => {
      // The bypass a registration-time check cannot catch: a public host that 302s to the metadata
      // endpoint. Following it would defeat the whole guard.
      const bounce = redirector('http://169.254.169.254/');
      const s = await bootServer({ tag: 'wh-redirect' });
      try {
        const made = (await (
          await register(s, {
            name: 'r',
            events: ['*'],
            config: { url: bounce.url },
          })
        ).json()) as { notifier: { id: number } };
        const res = (await (
          await fetch(`${s.base}/api/notifiers/${made.notifier.id}/test`, { method: 'POST', headers: s.H })
        ).json()) as { result: { ok: boolean; error?: string } };
        expect(res.result.ok).toBe(false);
        expect(res.result.error).toMatch(/redirect/);
      } finally {
        bounce.stop();
        await s.stop();
      }
    });
  });

  describe('delivery', () => {
    test('a real event is signed, deduplicable, and carries no credential', async () => {
      const rx = receiver();
      const s = await bootServer({ tag: 'wh-sign' });
      try {
        const made = (await (
          await register(s, { name: 'e2e', events: ['*'], config: { url: rx.url } })
        ).json()) as { secret: string; notifier: { id: number } };

        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({
            spec: 'version: 1\nstack: s\nenv:\n  API_TOKEN: super-secret-value\naxes:\n  - name: a\n    up: "echo hi"\n    assert_gone: "true"\n',
          }),
        });
        // Delivery is off the awaited path by design, so poll rather than assume.
        for (let i = 0; i < 40 && rx.got.length === 0; i++) await Bun.sleep(50);
        expect(rx.got.length).toBeGreaterThan(0);

        const d = rx.got[0]!;
        expect(d.event).toBe('deployment.created');
        // The signature verifies against an INDEPENDENT recomputation over `${ts}.${rawBody}` — the
        // body is signed exactly as sent, which is what stops "verifies here, fails there".
        const expected = `sha256=${new Bun.CryptoHasher('sha256', made.secret).update(`${d.timestamp}.${d.raw}`).digest('hex')}`;
        expect(d.signature).toBe(expected);
        // Timestamp is inside the signed material — that is the replay protection.
        expect(Math.abs(Date.now() - Number(d.timestamp))).toBeLessThan(60_000);
        expect(d.delivery).toBe(d.body!.id);

        // The spec declared a secret env var. It must appear nowhere in what was sent.
        expect(d.raw).not.toContain('super-secret-value');
        expect(d.raw).not.toContain(made.secret);
      } finally {
        rx.stop();
        await s.stop();
      }
    });

    test('a retry STOPS once an attempt succeeds — it does not run out the schedule', async () => {
      /*
       * `receiver({ failFirst })` was built for this and never called with it: every retry test
       * failed all three attempts, so nothing distinguished "retries until success" from "always
       * retries three times". The delivery log is what an operator reads to decide whether an
       * endpoint is healthy, and a successful-on-attempt-2 delivery recorded as 3 attempts, or as
       * failed, is a wrong answer to that question.
       */
      const rx = receiver({ status: (n) => (n <= 1 ? 500 : 200) });
      const s = await bootServer({ tag: 'wh-retry-ok' });
      try {
        const made = (await (
          await register(s, { name: 'flaky', events: ['*'], config: { url: rx.url } })
        ).json()) as { notifier: { id: number } };
        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ spec: 'version: 1\nstack: s\naxes: []\n' }),
        });

        let log: { deliveries: Array<{ status: string; attempts: number; responseCode: number | null }> } = {
          deliveries: [],
        };
        for (let i = 0; i < 60; i++) {
          log = (await (
            await fetch(`${s.base}/api/notifiers/${made.notifier.id}/deliveries`, { headers: s.H })
          ).json()) as typeof log;
          if (log.deliveries?.[0]?.status === 'ok') break;
          await Bun.sleep(150);
        }
        expect(log.deliveries?.[0]?.status).toBe('ok');
        expect(log.deliveries?.[0]?.attempts).toBe(2);
        expect(log.deliveries?.[0]?.responseCode).toBe(200);
        // The third attempt must never have been made — that is the whole claim.
        expect(rx.calls()).toBe(2);
      } finally {
        rx.stop();
        await s.stop();
      }
    }, 20_000);

    test('a spec write and a routing write actually reach a subscriber', async () => {
      // Every name in EVENTS must have an emit site. `spec.stored`, `spec.deleted` and
      // `routing.changed` were advertised, offered in the UI picker and accepted at registration —
      // and emitted from nowhere, so subscribing to them bought permanent silence.
      const rx = receiver();
      const routingDir = tmpd('routing');
      mkdirSync(routingDir, { recursive: true });
      const s = await bootServer({ tag: 'wh-catalogue', routingDir });
      try {
        await register(s, {
          name: 'specs',
          events: ['spec.stored', 'spec.deleted', 'routing.changed'],
          config: { url: rx.url },
        });
        const put = await fetch(`${s.base}/api/specs/s1`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ spec: 'version: 1\nstack: s\naxes:\n  - name: a\n    up: "true"\n' }),
        });
        expect(put.status).toBe(201);
        const route = await fetch(`${s.base}/api/routing/r1.yml`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ content: 'http:\n  routers: {}\n' }),
        });
        expect(route.status).toBe(201);
        expect((await fetch(`${s.base}/api/specs/s1`, { method: 'DELETE', headers: s.H })).status).toBe(200);

        for (let i = 0; i < 60 && rx.got.length < 3; i++) await Bun.sleep(50);
        expect(rx.got.map((g) => g.event).sort()).toEqual(['routing.changed', 'spec.deleted', 'spec.stored']);
        // The routing FILE's content can hold basicAuth hashes — only its name travels.
        const routing = rx.got.find((g) => g.event === 'routing.changed')!;
        expect(routing.body!.data).toEqual({ file: 'r1.yml', action: 'created' });
      } finally {
        rx.stop();
        await s.stop();
        rmSync(routingDir, { recursive: true, force: true });
      }
    });

    test('a failing endpoint is retried with the SAME delivery id, then logged as failed', async () => {
      // Fails every attempt, so all three are exercised.
      const rx = receiver({ status: 500 });
      const s = await bootServer({ tag: 'wh-retry-fail' });
      try {
        const made = (await (
          await register(s, { name: 'flaky', events: ['*'], config: { url: rx.url } })
        ).json()) as { notifier: { id: number } };
        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ spec: 'version: 1\nstack: s\naxes: []\n' }),
        });
        // Worst case is ~1s + 5s of backoff plus request time.
        for (let i = 0; i < 160 && rx.calls() < 3; i++) await Bun.sleep(50);
        expect(rx.calls()).toBe(3);
        // Stable across retries — a fresh id per attempt would make the receiver's dedupe useless.
        expect(new Set(rx.got.map((g) => g.delivery)).size).toBe(1);

        const log = (await (
          await fetch(`${s.base}/api/notifiers/${made.notifier.id}/deliveries`, { headers: s.H })
        ).json()) as { deliveries: Array<{ status: string; attempts: number; responseCode: number }> };
        expect(log.deliveries[0]!.status).toBe('failed');
        expect(log.deliveries[0]!.attempts).toBe(3);
        expect(log.deliveries[0]!.responseCode).toBe(500);
      } finally {
        rx.stop();
        await s.stop();
      }
      // 20s, because this exercises the REAL backoff (1s + 5s between three attempts) rather than a
      // test-only timing path. A shortened-for-tests schedule would leave the shipped one unverified.
    }, 20_000);

    test('a job that leaks names the leaked axes, and says whether anything was proven', async () => {
      const rx = receiver();
      const s = await bootServer({ tag: 'wh-leak' });
      try {
        await register(s, { name: 'leaks', events: ['job.leaked'], config: { url: rx.url } });
        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          // `assert_gone: false` — the resource is still there after teardown.
          body: JSON.stringify({
            spec: 'version: 1\nstack: s\naxes:\n  - name: database\n    down: "true"\n    assert_gone: "false"\n',
          }),
        });
        await fetch(`${s.base}/api/deployments/d/down`, { method: 'POST', headers: s.H, body: '{}' });
        for (let i = 0; i < 80 && rx.got.length === 0; i++) await Bun.sleep(50);

        expect(rx.got.length).toBe(1);
        const data = rx.got[0]!.body!.data as { leakedAxes: string[]; verified: boolean; state: string };
        expect(rx.got[0]!.event).toBe('job.leaked');
        expect(data.state).toBe('leaked');
        // The axis NAMES are the operator-actionable part.
        expect(data.leakedAxes).toEqual(['database']);
        expect(data.verified).toBe(true);
      } finally {
        rx.stop();
        await s.stop();
      }
    });

    test('`verified: false` when nothing was actually checked', async () => {
      // `down` with verify:false emits zero assert_gone steps, so the leak check can never be true
      // and the job reports success having proven nothing. Without this field `job.succeeded` would
      // read as "clean" when it means "nobody looked".
      const rx = receiver();
      const s = await bootServer({ tag: 'wh-unverified' });
      try {
        await register(s, { name: 'all', events: ['*'], config: { url: rx.url } });
        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({
            spec: 'version: 1\nstack: s\naxes:\n  - name: a\n    down: "true"\n    assert_gone: "true"\n',
          }),
        });
        await fetch(`${s.base}/api/deployments/d/down`, {
          method: 'POST',
          headers: s.H,
          body: JSON.stringify({ verify: false }),
        });
        for (let i = 0; i < 80; i++) {
          if (rx.got.some((g) => g.event === 'job.succeeded')) break;
          await Bun.sleep(50);
        }
        const done = rx.got.find((g) => g.event === 'job.succeeded')!;
        expect(done).toBeDefined();
        expect((done.body!.data as { verified: boolean }).verified).toBe(false);
      } finally {
        rx.stop();
        await s.stop();
      }
    });
  });

  describe('registration', () => {
    test('the signing secret is returned once and never again', async () => {
      const s = await bootServer({ tag: 'wh-secret' });
      try {
        const made = (await (
          await register(s, { name: 'once', events: ['*'], config: { url: 'https://example.com/h' } })
        ).json()) as { secret: string; notifier: { id: number } };
        expect(made.secret.startsWith('whsec_')).toBe(true);

        for (const path of [
          '/api/notifiers',
          `/api/notifiers/${made.notifier.id}/deliveries`,
          '/api/notifiers/meta',
        ]) {
          const raw = await (await fetch(`${s.base}${path}`, { headers: s.H })).text();
          expect(`${path}: ${raw.includes(made.secret)}`).toBe(`${path}: false`);
        }
      } finally {
        await s.stop();
      }
    });

    test('an unsubscribable event name is refused, listing the real ones', async () => {
      const s = await bootServer({ tag: 'wh-typo' });
      try {
        const r = await register(s, {
          name: 'typo',
          events: ['job.leeked'],
          config: { url: 'https://example.com/h' },
        });
        expect(r.status).toBe(400);
        const err = ((await r.json()) as { error: string }).error;
        expect(err).toContain('job.leeked');
        expect(err).toContain('job.leaked');
      } finally {
        await s.stop();
      }
    });

    test('an unknown notifier type is refused by name', async () => {
      // `slack` was the example unknown type here until 0.20.0 made it real — the seam's roadmap
      // catching up with its own test fixture.
      const s = await bootServer({ tag: 'wh-teams' });
      try {
        const r = await register(s, {
          name: 'teams-someday',
          type: 'teams',
          events: ['*'],
          config: { url: 'https://example.com/h' },
        });
        expect(r.status).toBe(400);
        expect(((await r.json()) as { error: string }).error).toMatch(/unknown notifier type "teams"/);
      } finally {
        await s.stop();
      }
    });

    test('/meta drives the UI: event names and per-type form fields come from the server', async () => {
      // The composability seam reaching the UI — adding a type must not mean editing the UI.
      const s = await bootServer({ tag: 'wh-meta' });
      try {
        const meta = (await (await fetch(`${s.base}/api/notifiers/meta`, { headers: s.H })).json()) as {
          events: string[];
          wildcard: string;
          types: Array<{ kind: string; label: string; fields: Array<{ key: string }> }>;
        };
        expect(meta.events).toContain('job.leaked');
        expect(meta.wildcard).toBe('*');
        expect(meta.types.map((t) => t.kind)).toEqual(['webhook', 'slack', 'discord']);
        expect(meta.types[0]!.fields.map((f) => f.key)).toEqual(['url']);
        // Chat types: one field, marked secret (the URL IS the credential), and no signing.
        for (const kind of ['slack', 'discord'] as const) {
          const ty = meta.types.find((x) => x.kind === kind)! as unknown as {
            signs: boolean;
            fields: Array<{ key: string; secret?: boolean }>;
          };
          expect(ty.signs).toBe(false);
          expect(ty.fields.map((f) => f.key)).toEqual(['webhookUrl']);
          expect(ty.fields[0]!.secret).toBe(true);
        }
      } finally {
        await s.stop();
      }
    });

    test('disabled notifiers receive nothing', async () => {
      const rx = receiver();
      const s = await bootServer({ tag: 'wh-disabled' });
      try {
        const made = (await (
          await register(s, { name: 'off', events: ['*'], config: { url: rx.url } })
        ).json()) as { notifier: { id: number } };
        await fetch(`${s.base}/api/notifiers/${made.notifier.id}`, {
          method: 'PATCH',
          headers: s.H,
          body: JSON.stringify({ enabled: false }),
        });
        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ spec: 'version: 1\nstack: s\naxes: []\n' }),
        });
        await Bun.sleep(400);
        expect(rx.got).toEqual([]);

        /*
         * THE POSITIVE CONTROL. "Nothing arrived" is also what a completely broken delivery path
         * looks like, so on its own the assertion above proves nothing. Re-enable the SAME notifier
         * and the SAME receiver, fire the SAME kind of event: if this does not arrive, the test
         * above was passing for the wrong reason.
         */
        await fetch(`${s.base}/api/notifiers/${made.notifier.id}`, {
          method: 'PATCH',
          headers: s.H,
          body: JSON.stringify({ enabled: true }),
        });
        await fetch(`${s.base}/api/deployments/d2`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ spec: 'version: 1\nstack: s2\naxes: []\n' }),
        });
        for (let i = 0; i < 40 && rx.got.length === 0; i++) await Bun.sleep(50);
        expect(rx.got.map((g) => g.event)).toEqual(['deployment.created']);
      } finally {
        rx.stop();
        await s.stop();
      }
    });

    test('every notifier route requires auth', async () => {
      const s = await bootServer({ tag: 'wh-auth' });
      try {
        for (const [method, path] of [
          ['GET', '/api/notifiers'],
          ['POST', '/api/notifiers'],
          ['GET', '/api/notifiers/meta'],
          ['GET', '/api/notifiers/1/deliveries'],
        ] as const) {
          const r = await fetch(`${s.base}${path}`, { method, body: method === 'POST' ? '{}' : undefined });
          expect(`${method} ${path} → ${r.status}`).toBe(`${method} ${path} → 401`);
        }
      } finally {
        await s.stop();
      }
    });
  });
});

describe('delivery queueing and redelivery', () => {
  /*
   * The receivers below are on 127.0.0.1, which the notifier URL check refuses by default (see the
   * SSRF note in notify.ts). The harness boots every server with PSTACK_NOTIFY_ALLOW_PRIVATE=1.
   */
  const registerAll = async (s: Booted, url: string) => {
    const r = await fetch(`${s.base}/api/notifiers`, {
      method: 'POST',
      headers: s.H,
      body: JSON.stringify({ name: 'r', type: 'webhook', events: ['*'], config: { url } }),
    });
    return ((await r.json()) as { notifier: { id: number } }).notifier.id;
  };

  /** One real event per call: a spec PUT emits `spec.stored`. */
  const storeSpec = (s: Booted, name: string) =>
    fetch(`${s.base}/api/specs/${name}`, {
      method: 'PUT',
      headers: s.H,
      body: JSON.stringify({ spec: 'version: 1\nstack: s\naxes:\n  - name: a\n    up: "true"\n' }),
    });

  test('a burst QUEUES instead of dropping — the "still in progress" report', async () => {
    /*
     * The reported bug. One delivery can take 21s (3 attempts × 5s + backoff) and only one runs per
     * notifier at a time, so a burst — readiness alone emits five events within seconds of a deploy —
     * used to write every event after the first straight to the log as
     * "dropped — a delivery to this notifier is still in progress", with nothing ever retrying it.
     *
     * Five spec PUTs back to back are the burst here; the receiver is slower than the gap between
     * them, so the second event arrives while the first delivery is still in flight.
     */
    const rx = receiver({ delayMs: 120 });
    const s = await bootServer({ tag: 'wh-burst' });
    try {
      const id = await registerAll(s, rx.url);
      for (let i = 0; i < 5; i++) await storeSpec(s, `s${i}`);

      for (let i = 0; i < 200 && rx.got.length < 5; i++) await Bun.sleep(50);
      expect(rx.got.length).toBe(5); // every one arrived…

      // The receiver records a request on arrival, before its slow reply, so the last delivery is
      // still in flight when the fifth is counted — wait for the LOG to show all five finished.
      type Log = { deliveries: Array<{ status: string; error: string | null }> };
      const body = await until(
        async () => (await (await fetch(`${s.base}/api/notifiers/${id}/deliveries`, { headers: s.H })).json()) as Log,
        (l) => (l.deliveries ?? []).filter((d) => d.status === 'ok').length >= 5,
      );
      // …and none was recorded as dropped.
      expect(body.deliveries.filter((d) => d.status === 'ok').length).toBe(5);
      expect(body.deliveries.some((d) => (d.error ?? '').includes('still in progress'))).toBe(false);
    } finally {
      await s.stop();
      rx.stop();
    }
  }, 30_000);

  test('a delivery can be replayed: same event id, fresh timestamp, marked as a replay', async () => {
    const rx = receiver();
    const s = await bootServer({ tag: 'wh-replay' });
    try {
      // Only `job.leaked`, so the deployment PUT and `job.started` on the way to it are not in the log.
      const r = await fetch(`${s.base}/api/notifiers`, {
        method: 'POST',
        headers: s.H,
        body: JSON.stringify({ name: 'r', type: 'webhook', events: ['job.leaked'], config: { url: rx.url } }),
      });
      const id = ((await r.json()) as { notifier: { id: number } }).notifier.id;
      // A real leak: `down` lies and `assert_gone` says the db is still there.
      await fetch(`${s.base}/api/deployments/d`, {
        method: 'PUT',
        headers: s.H,
        body: JSON.stringify({
          spec: 'version: 1\nstack: shopfront-pr-7\naxes:\n  - name: db\n    down: "true"\n    assert_gone: "false"\n',
        }),
      });
      await fetch(`${s.base}/api/deployments/d/down`, { method: 'POST', headers: s.H, body: '{}' });
      for (let i = 0; i < 100 && rx.got.length < 1; i++) await Bun.sleep(50);
      expect(rx.got.length).toBe(1);
      const first = JSON.parse(rx.got[0]!.raw) as { id: string; event: string; data: { stack: string } };

      const list = (await (
        await fetch(`${s.base}/api/notifiers/${id}/deliveries`, { headers: s.H })
      ).json()) as { deliveries: Array<{ id: number; replayable: boolean }>; queued: number };
      expect(list.deliveries[0]!.replayable).toBe(true);

      const rd = await fetch(
        `${s.base}/api/notifiers/${id}/deliveries/${list.deliveries[0]!.id}/redeliver`,
        { method: 'POST', headers: s.H },
      );
      expect(rd.status).toBe(200);
      for (let i = 0; i < 100 && rx.got.length < 2; i++) await Bun.sleep(50);
      expect(rx.got.length).toBe(2);

      const replay = JSON.parse(rx.got[1]!.raw) as { id: string; event: string; data: { stack: string } };
      // The SAME event, so a receiver that already handled it dedupes on the id it saw before.
      expect(replay.id).toBe(first.id);
      expect(replay.event).toBe('job.leaked');
      expect(replay.data.stack).toBe('shopfront-pr-7');
      // …flagged, so a receiver that did NOT record it can tell this is a replay.
      expect(rx.got[1]!.redelivery).toBe('1');
      expect(rx.got[0]!.redelivery).toBeNull();
      // Fresh timestamp: the signature covers it and receivers reject stale ones, so replaying the
      // original stamp would be refused by a correct receiver.
      expect(Number(rx.got[1]!.timestamp)).toBeGreaterThanOrEqual(Number(rx.got[0]!.timestamp));
    } finally {
      await s.stop();
      rx.stop();
    }
  }, 30_000);

  test("another notifier's delivery cannot be replayed through this one", async () => {
    const rx = receiver();
    const s = await bootServer({ tag: 'wh-cross' });
    try {
      const a = await registerAll(s, rx.url);
      const b = await registerAll(s, rx.url);
      await storeSpec(s, 's1');
      for (let i = 0; i < 100 && rx.got.length < 2; i++) await Bun.sleep(50);

      const aList = (await (
        await fetch(`${s.base}/api/notifiers/${a}/deliveries`, { headers: s.H })
      ).json()) as { deliveries: Array<{ id: number }> };
      // A's delivery id, aimed at B.
      const r = await fetch(
        `${s.base}/api/notifiers/${b}/deliveries/${aList.deliveries[0]!.id}/redeliver`,
        { method: 'POST', headers: s.H },
      );
      expect(r.status).toBe(404);
      // …and an unauthenticated replay never happens at all.
      const un = await fetch(`${s.base}/api/notifiers/${a}/deliveries/${aList.deliveries[0]!.id}/redeliver`, {
        method: 'POST',
      });
      expect(un.status).toBe(401);
    } finally {
      await s.stop();
      rx.stop();
    }
  }, 30_000);
});
