/**
 * Fake HTTP endpoints pstack talks TO: webhook receivers (signature-verifying, chat-shaped,
 * slow, redirecting). All on 127.0.0.1 port 0; all capture the raw bytes, because a signature is
 * over bytes.
 */
export type Delivery = {
  event: string;
  delivery: string;
  timestamp: string;
  signature: string;
  redelivery: string | null;
  raw: string;
  headers: Record<string, string>;
  body: { id: string; event: string; at: number; data: Record<string, unknown> } | null;
};

export type Receiver = {
  url: string;
  got: Delivery[];
  calls: () => number;
  /** Block until `n` deliveries arrived (or `ms` elapsed). */
  waitFor: (n: number, ms?: number) => Promise<Delivery[]>;
  stop: () => void;
};

export function receiver(o: { status?: number | ((n: number) => number); delayMs?: number; path?: string } = {}): Receiver {
  const got: Delivery[] = [];
  let calls = 0;
  const s = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    async fetch(req) {
      calls++;
      const raw = await req.text();
      let body: Delivery['body'] = null;
      try {
        body = JSON.parse(raw);
      } catch {
        /* chat payloads are not the envelope */
      }
      const headers: Record<string, string> = {};
      req.headers.forEach((v, k) => (headers[k] = v));
      // Recorded AFTER the reply is decided (and after any delay): `got.length === n` then means
      // the nth delivery has been answered, which is what a queue test needs to know.
      if (o.delayMs) await Bun.sleep(o.delayMs);
      const status = typeof o.status === 'function' ? o.status(calls) : (o.status ?? 200);
      got.push({
        event: req.headers.get('x-pstack-event') ?? '',
        delivery: req.headers.get('x-pstack-delivery') ?? '',
        timestamp: req.headers.get('x-pstack-timestamp') ?? '',
        signature: req.headers.get('x-pstack-signature') ?? '',
        redelivery: req.headers.get('x-pstack-redelivery'),
        raw,
        headers,
        body,
      });
      return new Response('', { status });
    },
  });
  return {
    url: `http://127.0.0.1:${s.port}${o.path ?? '/hook'}`,
    got,
    calls: () => calls,
    waitFor: async (n, ms = 5000) => {
      const deadline = Date.now() + ms;
      while (got.length < n && Date.now() < deadline) await Bun.sleep(10);
      return got;
    },
    stop: () => s.stop(true),
  };
}

/** A server that answers 302 to wherever you say — the SSRF-by-redirect probe. */
export function redirector(to: string): { url: string; stop: () => void } {
  const s = Bun.serve({ port: 0, hostname: '127.0.0.1', fetch: () => Response.redirect(to, 302) });
  return { url: `http://127.0.0.1:${s.port}/r`, stop: () => s.stop(true) };
}

/** Verify a delivery the way an independent receiver would: HMAC-SHA256 over `<ts>.<raw>`. */
export function verifySignature(secret: string, d: Delivery): boolean {
  const mac = new Bun.CryptoHasher('sha256', secret).update(`${d.timestamp}.${d.raw}`).digest('hex');
  return d.signature === `sha256=${mac}`;
}

/**
 * THE EVENT TAP. Domain events are observed the way any receiver observes them — through a
 * registered `*` webhook notifier — rather than through a test-only route. Register it, then
 * `tap.events()` is the ordered list of what the server emitted.
 */
export type Tap = Receiver & {
  events: () => string[];
  notifierId: number;
  /** The payloads of every delivery of that event so far. */
  dataOf: (event: string) => Array<Record<string, unknown>>;
  /** Block until at least `n` deliveries of `event` arrived. */
  waitForEvent: (event: string, n?: number, ms?: number) => Promise<Array<Record<string, unknown>>>;
};

export async function eventTap(base: string, H: Record<string, string>): Promise<Tap> {
  const r = receiver();
  const res = await fetch(`${base}/api/notifiers`, {
    method: 'POST',
    headers: H,
    body: JSON.stringify({ type: 'webhook', name: 'conformance-tap', config: { url: r.url }, events: ['*'] }),
  });
  if (res.status !== 201) throw new Error(`could not register the event tap: ${res.status} ${await res.text()}`);
  const { notifier } = (await res.json()) as { notifier: { id: number } };
  const dataOf = (event: string) => r.got.filter((d) => d.event === event).map((d) => d.body?.data ?? {});
  return Object.assign(r, {
    events: () => r.got.map((d) => d.event),
    notifierId: notifier.id,
    dataOf,
    waitForEvent: async (event: string, n = 1, ms = 5000) => {
      const deadline = Date.now() + ms;
      while (dataOf(event).length < n && Date.now() < deadline) await Bun.sleep(10);
      return dataOf(event);
    },
  });
}
