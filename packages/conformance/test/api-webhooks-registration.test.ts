/**
 * Webhooks — registration, the defences, and the chat types. Ported from
 * packages/pstack/test/stack.test.ts ('webhooks — composable notifications', up to 'the URL guard').
 *
 * Every server is spawned; receivers come from the harness. `PSTACK_NOTIFY_ALLOW_PRIVATE` is no
 * longer process-wide state to save and restore around each test — the harness sets it to '1' in
 * the child's environment, and nothing here needs it otherwise.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer } from '../harness/server.ts';
import { receiver } from '../harness/receiver.ts';

describe('webhooks — composable notifications', () => {
  const register = (base: string, H: Record<string, string>, body: Record<string, unknown>) =>
    fetch(`${base}/api/notifiers`, { method: 'POST', headers: H, body: JSON.stringify(body) });

  /**
   * A receiver that answers 302 to a Location the test chooses AFTER it knows the secret — the one
   * way to make a delivery error that CONTAINS the credential (harness `redirector()` fixes its
   * target at construction, before any secret exists).
   */
  const bouncer = () => {
    const b = { url: '', location: '', stop: () => {} };
    const s = Bun.serve({
      port: 0,
      hostname: '127.0.0.1',
      fetch: () => new Response('', { status: 302, headers: { location: b.location } }),
    });
    b.url = `http://127.0.0.1:${s.port}/services/T000/B111/XXXXXXXXXXXX`;
    b.stop = () => s.stop(true);
    return b;
  };

  /*
   * 'a second notifier type — the composability claim, actually exercised' is NOT here: it registers
   * a type into TYPES at runtime (in-process module state) and has no black-box equivalent. The
   * claim it guards — a type that does not sign gets no secret, is masked at rest, and still
   * delivers the real credential — is exercised below by the two chat types that shipped through
   * the same seam.
   */

  describe('the defences that had no test', () => {
    test('redaction strips the signing secret AND every config string', async () => {
      /*
       * The original tested `redactForNotifier` DIRECTLY, and said why: its first version drove a
       * real delivery at a dead port and asserted the log was clean — and passed with the redaction
       * ripped out, because Bun's connection error is "Unable to connect…" and never contained the
       * credential to begin with. A test whose subject cannot appear in the input is not testing
       * anything.
       *
       * Black-box, the input CAN carry the credential: a receiver that answers 302 puts whatever it
       * likes in `Location`, and the stored error is "redirect to <Location> not followed". So the
       * receiver is told the secret and its own URL, and the error that comes back through the API
       * must have lost both.
       *
       * This is the behaviour notify.ts defends at length: not just the secret, but EVERY string in
       * `config`, so the Slack URL and a future SMTP password are covered by an author who never
       * read the comment.
       */
      const rx = bouncer();
      const plain = receiver({ status: 500 });
      const s = await bootServer({ tag: 'wh-redact' });
      try {
        const config = { url: rx.url, channel: '#ops' };
        const made = await register(s.base, s.H, { name: 'redact', events: ['*'], config });
        expect(made.status).toBe(201);
        const { notifier, secret } = (await made.json()) as { notifier: { id: number }; secret: string };
        expect(secret).toMatch(/^whsec_/);
        rx.location = `POST ${config.url} failed; signed with ${secret}; posting to ${config.channel}`;

        const tested = (await (
          await fetch(`${s.base}/api/notifiers/${notifier.id}/test`, { method: 'POST', headers: s.H })
        ).json()) as { result: { ok: boolean; error?: string } };
        expect(tested.result.ok).toBe(false);
        const out = tested.result.error ?? '';
        expect(out).toContain('redirect to');
        expect(out).not.toContain(secret);
        expect(out).not.toContain(config.url);
        // `redactText` ignores strings under 8 characters, so a channel name survives intact — the
        // log would be useless if every short config value became a blob.
        expect(out).toContain('#ops');

        // The stored row went through the same function: the log is the read path a UI shows.
        const log = (await (
          await fetch(`${s.base}/api/notifiers/${notifier.id}/deliveries`, { headers: s.H })
        ).json()) as { deliveries: Array<{ status: string; error: string | null }> };
        expect(log.deliveries[0]?.status).toBe('failed');
        expect(log.deliveries[0]?.error).not.toContain(secret);
        expect(log.deliveries[0]?.error).not.toContain(config.url);
        expect(log.deliveries[0]?.error).toContain('#ops');

        // A message with nothing to hide is returned unchanged.
        const other = (await (
          await register(s.base, s.H, { name: 'plain', events: ['*'], config: { url: plain.url } })
        ).json()) as { notifier: { id: number } };
        const clean = (await (
          await fetch(`${s.base}/api/notifiers/${other.notifier.id}/test`, { method: 'POST', headers: s.H })
        ).json()) as { result: { error?: string } };
        expect(clean.result.error).toBe('HTTP 500');
      } finally {
        rx.stop();
        plain.stop();
        await s.stop();
      }
    }, 20_000);

    test('a job event carries status, never the outcome that holds captured credentials', async () => {
      /*
       * `outcome.outputs` is the documented inter-axis env channel — a provisioned database's
       * connection string lives there BY DESIGN (docs/secret-exposure.md). jobs.ts builds its
       * payload field by field specifically so the outcome cannot ride along, and nothing checked.
       */
      const rx = receiver();
      const s = await bootServer({ tag: 'wh-job' });
      try {
        await register(s.base, s.H, { name: 'jobs', events: ['*'], config: { url: rx.url } });
        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({
            spec:
              'version: 1\nstack: s\nenv:\n  DB_PASSWORD: hunter2-in-the-env\n' +
              'axes:\n  - name: a\n    up: "echo NOTHING"\n    assert_gone: "true"\n',
          }),
        });
        await fetch(`${s.base}/api/deployments/d/verify`, { method: 'POST', headers: s.H });
        for (let i = 0; i < 60 && !rx.got.some((g) => g.event.startsWith('job.')); i++) await Bun.sleep(100);

        const jobEvents = rx.got.filter((g) => g.event.startsWith('job.'));
        expect(jobEvents.length).toBeGreaterThan(0);
        for (const e of jobEvents) {
          expect(e.raw).not.toContain('hunter2-in-the-env');
          // The whole object, not just the secret: `outcome` must not be in the payload at all.
          expect(Object.keys(e.body!.data)).not.toContain('outcome');
          expect(Object.keys(e.body!.data)).not.toContain('outputs');
        }
      } finally {
        rx.stop();
        await s.stop();
      }
    }, 30_000);

    /*
     * 'a listener that throws — synchronously or asynchronously — cannot reach the emitter' is NOT
     * here: it registers listeners on the in-process bus. Black-box, the closest observable is that
     * the server survives a delivery that fails — which every failing-receiver test in this suite
     * already proves by making a second request afterwards.
     */
  });

  describe('Slack and Discord — the chat types the seam was built for', () => {
    /** A chat receiver: no signature verification (there is none to verify), captures raw JSON. */
    const chatReceiver = (status = 200) => {
      const rx = receiver({ status });
      // The path IS the credential for these types; the masking assertion below greps for it.
      return Object.assign(rx, { url: rx.url.replace(/\/hook$/, '/services/T00/B00/tok') });
    };

    test('a Slack delivery is a readable line under `text`, unsigned, with the URL masked at rest', async () => {
      const rx = chatReceiver(200);
      const s = await bootServer({ tag: 'wh-slack' });
      try {
        const made = await register(s.base, s.H, {
          name: 'ops',
          type: 'slack',
          events: ['*'],
          config: { webhookUrl: rx.url },
        });
        expect(made.status).toBe(201);
        // No HMAC secret for a type whose URL is the credential.
        expect(((await made.json()) as { secret: string | null }).secret).toBeNull();

        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ spec: 'version: 1\nstack: chat-demo\naxes: []\n' }),
        });
        for (let i = 0; i < 40 && rx.got.length === 0; i++) await Bun.sleep(50);
        expect(rx.got).toHaveLength(1);

        const d = rx.got[0]!;
        const body = JSON.parse(d.raw) as { text?: string };
        // A sentence a human reads in a channel — not an envelope of raw JSON.
        expect(body.text).toBe('Deployment d created (stack chat-demo).');
        // Unsigned by design: the URL is the credential, and a signature header would imply a
        // secret the operator was never given.
        expect(d.headers['x-pstack-signature']).toBeUndefined();

        // At rest the URL is credential material: masked in the list, absent verbatim everywhere.
        const listed = await (await fetch(`${s.base}/api/notifiers`, { headers: s.H })).text();
        expect(listed).not.toContain('/services/T00/B00/tok');
      } finally {
        rx.stop();
        await s.stop();
      }
    }, 20_000);

    test('a Discord delivery posts `content` and treats its 204 as success', async () => {
      const rx = chatReceiver(204); // Discord answers 204 No Content on success
      const s = await bootServer({ tag: 'wh-discord' });
      try {
        const made = (await (
          await register(s.base, s.H, {
            name: 'discord-ops',
            type: 'discord',
            events: ['*'],
            config: { webhookUrl: rx.url },
          })
        ).json()) as { notifier: { id: number } };

        await fetch(`${s.base}/api/deployments/d2`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({ spec: 'version: 1\nstack: chat-demo-2\naxes: []\n' }),
        });
        for (let i = 0; i < 40 && rx.got.length === 0; i++) await Bun.sleep(50);
        expect((JSON.parse(rx.got[0]!.raw) as { content?: string }).content).toBe(
          'Deployment d2 created (stack chat-demo-2).',
        );

        // The 204 must be recorded as ok — a strict ===200 check would log every successful
        // Discord delivery as failed.
        let status = '';
        for (let i = 0; i < 40 && status !== 'ok'; i++) {
          const log = (await (
            await fetch(`${s.base}/api/notifiers/${made.notifier.id}/deliveries`, { headers: s.H })
          ).json()) as { deliveries: Array<{ status: string; responseCode: number | null }> };
          status = log.deliveries[0]?.status ?? '';
          if (status !== 'ok') await Bun.sleep(50);
        }
        expect(status).toBe('ok');
      } finally {
        rx.stop();
        await s.stop();
      }
    }, 20_000);

    test('a test delivery reads as a test — never as a job that succeeded', async () => {
      // The 0.14.0 contract: `data.test === true` must be honoured by per-event formatters,
      // because the synthesized event name is a real one.
      const rx = chatReceiver(200);
      const s = await bootServer({ tag: 'wh-test' });
      try {
        const made = (await (
          await register(s.base, s.H, {
            name: 'slack-test',
            type: 'slack',
            events: ['*'],
            config: { webhookUrl: rx.url },
          })
        ).json()) as { notifier: { id: number } };
        await fetch(`${s.base}/api/notifiers/${made.notifier.id}/test`, { method: 'POST', headers: s.H });
        for (let i = 0; i < 40 && rx.got.length === 0; i++) await Bun.sleep(50);
        const text = (JSON.parse(rx.got[0]!.raw) as { text?: string }).text ?? '';
        expect(text).toContain('Test delivery');
        expect(text).not.toMatch(/succeeded/i);
      } finally {
        rx.stop();
        await s.stop();
      }
    }, 20_000);

    test('the leaked wording cannot be skimmed past as routine', async () => {
      // The original called `summarize()` on a hand-built job.leaked. Black-box, the event comes
      // from a real leak: two axes whose `assert_gone` fails, and a verify that proves it.
      const rx = chatReceiver(200);
      const s = await bootServer({ tag: 'wh-leaked' });
      try {
        await register(s.base, s.H, {
          name: 'leaks',
          type: 'slack',
          events: ['job.leaked'],
          config: { webhookUrl: rx.url },
        });
        await fetch(`${s.base}/api/deployments/d`, {
          method: 'PUT',
          headers: s.H,
          body: JSON.stringify({
            spec:
              'version: 1\nstack: s\naxes:\n' +
              '  - name: db\n    up: "true"\n    assert_gone: "false"\n' +
              '  - name: dns\n    up: "true"\n    assert_gone: "false"\n',
          }),
        });
        await fetch(`${s.base}/api/deployments/d/verify`, { method: 'POST', headers: s.H });
        for (let i = 0; i < 60 && rx.got.length === 0; i++) await Bun.sleep(100);
        const line = (JSON.parse(rx.got[0]!.raw) as { text?: string }).text ?? '';
        expect(line).toContain('LEAKED');
        expect(line).toContain('db, dns');
        expect(line).toContain('nothing will retry');
      } finally {
        rx.stop();
        await s.stop();
      }
    }, 20_000);
  });
});
