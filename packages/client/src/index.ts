/**
 * A typed client for a pstack control plane.
 *
 * ── WHY IT IS HAND-WRITTEN ───────────────────────────────────────────────────────────────────────
 *
 * There is no OpenAPI document to generate from, and the API is a plain `Bun.serve` router rather
 * than a framework with an RPC client to borrow. Writing the ~30 calls out is smaller than adding a
 * spec, a generator and a build step to produce the same thing — and it keeps this package at ZERO
 * dependencies, which is what makes it safe to install into a CI job that also runs a deploy.
 *
 * The API is NOT changed to suit this client. Everything here is the existing surface, and the
 * anti-drift measure is `test/client.test.ts`: it boots the real server and drives this client
 * against it, so a route or field that moves fails a test instead of quietly lying in a `.d.ts`.
 *
 * ── IT THROWS ────────────────────────────────────────────────────────────────────────────────────
 *
 * Opposite of the web UI's client, on purpose. A page renders a 409 as content; a script wants the
 * line it wrote to either work or stop. Every non-2xx becomes a `PstackError` carrying the status and
 * the parsed body, so `catch (e) { if (e.status === 409) … }` is the whole error-handling story.
 *
 * ── WHAT IS WORTH INSTALLING THIS FOR ────────────────────────────────────────────────────────────
 *
 * `waitForJob`, `waitForReady` and `verifyWebhook` (below). They are the three things every user
 * would otherwise re-write, and the two waiters are the ones that are easy to get subtly wrong —
 * polling a job without a terminal-state list, or treating `readiness.state === 'watching'` as an
 * error rather than "ask again".
 */

import type {
  DeliveryRow,
  DeploymentRow,
  Health,
  HostVar,
  Job,
  Kind,
  Logs,
  NotifierRow,
  Readiness,
  Runtime,
  ShareLink,
  ShareView,
  SpecMeta,
  SsoConfig,
  SsoConfigResponse,
  SwarmInfo,
} from './types.ts';

export * from './types.ts';

/** A non-2xx answer. `body` is the parsed JSON, which is where the server's own message lives. */
export class PstackError extends Error {
  readonly status: number;
  readonly body: Record<string, unknown>;
  readonly url: string;

  constructor(status: number, url: string, body: Record<string, unknown>) {
    super(typeof body.error === 'string' ? body.error : `HTTP ${status} from ${url}`);
    this.name = 'PstackError';
    this.status = status;
    this.body = body;
    this.url = url;
  }
}

export type ClientOptions = {
  /** e.g. `https://api.preview.example.com`. A trailing slash is fine. */
  baseUrl: string;
  /**
   * `PSTACK_TOKEN` (the machine credential CI holds), a personal token (`pstack_pat_…`), or the
   * token of a share link (`deployments.share`) — which then reaches exactly the reads that link
   * allows, on that one deployment.
   *
   * Optional only because a server with no token configured is loopback-only dev mode. Anywhere
   * else, every route needs it.
   */
  token?: string;
  /** Swap in for tests, a proxy, or an agent with custom TLS. Defaults to global `fetch`. */
  fetch?: typeof fetch;
  /** Per request, in ms. Deploys are started, never awaited, so this bounds the HTTP call only. */
  timeoutMs?: number;
};

/** `{ PR: '7' }` → `?PR=7`. These are the spec's variables, and `down` needs the SAME ones as `up`. */
export type Vars = Record<string, string | number | undefined>;

function qs(vars: Vars | undefined, extra: Record<string, string | number | undefined> = {}): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(vars ?? {})) if (v !== undefined) p.set(k, String(v));
  for (const [k, v] of Object.entries(extra)) if (v !== undefined) p.set(k, String(v));
  const s = p.toString();
  return s ? `?${s}` : '';
}

const enc = encodeURIComponent;

export function createClient(opts: ClientOptions) {
  const base = opts.baseUrl.replace(/\/$/, '');
  const doFetch = opts.fetch ?? globalThis.fetch;

  async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const url = `${base}${path}`;
    const headers: Record<string, string> = {};
    if (opts.token) headers.authorization = `Bearer ${opts.token}`;
    if (body !== undefined) headers['content-type'] = 'application/json';
    const res = await doFetch(url, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: opts.timeoutMs ? AbortSignal.timeout(opts.timeoutMs) : undefined,
    });
    const text = await res.text();
    // A route that answers text (the swarm join material) is read as-is; everything else is JSON.
    if (res.ok && (res.headers.get('content-type') ?? '').startsWith('text/plain')) return text as unknown as T;
    let parsed: unknown = null;
    try {
      parsed = text ? JSON.parse(text) : null;
    } catch {
      // A non-JSON body means something other than this API answered — a proxy, an error page. Say
      // so with the text, rather than reporting a parse error about a page nobody asked for.
      throw new PstackError(res.status, url, {
        error: `expected JSON, got ${res.status}: ${text.slice(0, 300)}`,
      });
    }
    if (!res.ok) throw new PstackError(res.status, url, (parsed ?? {}) as Record<string, unknown>);
    return (parsed ?? {}) as T;
  }

  const get = <T>(p: string) => request<T>('GET', p);
  const post = <T>(p: string, b?: unknown) => request<T>('POST', p, b ?? {});
  const put = <T>(p: string, b: unknown) => request<T>('PUT', p, b);
  const del = <T>(p: string) => request<T>('DELETE', p);

  const client = {
    /** Escape hatch: any route this client does not wrap yet, with auth and error handling applied. */
    request,

    health: () => get<Health>('/api/health'),

    deployments: {
      list: (vars?: Vars) => get<{ deployments: DeploymentRow[] }>(`/api/deployments${qs(vars)}`).then((r) => r.deployments),
      get: (id: string, vars?: Vars) => get<DeploymentRow & { spec?: unknown }>(`/api/deployments/${enc(id)}${qs(vars)}`),

      /**
       * Submit or replace. `spec` is an inline spec file; `specName` references a stored one — one
       * or the other, never both, because a deployment has one source of truth.
       */
      put: (
        id: string,
        body: { spec?: string; specName?: string; compose?: string; env?: Record<string, string>; vars?: Record<string, string> },
      ) => put<{ id: string; kind: Kind; stack: string }>(`/api/deployments/${enc(id)}`, body),

      /** Forget the RECORD. Never tears anything down, and is refused while containers exist. */
      remove: (id: string, vars?: Vars) => del<{ removed: string; stack: string }>(`/api/deployments/${enc(id)}${qs(vars)}`),

      /** Starts a job and returns immediately. Follow it with `waitForJob`. */
      up: (id: string, vars?: Vars) => post<{ job: Job }>(`/api/deployments/${enc(id)}/up${qs(vars)}`).then((r) => r.job),
      verify: (id: string, vars?: Vars) => post<{ job: Job }>(`/api/deployments/${enc(id)}/verify${qs(vars)}`).then((r) => r.job),
      /**
       * `force` is required for a `kind: shared` stack and is refused without it — `down` runs
       * `compose down -v`, which on a shared deployment destroys the volumes every tenant depends on.
       */
      down: (id: string, body: { verify?: boolean; force?: boolean } = {}, vars?: Vars) =>
        post<{ job: Job }>(`/api/deployments/${enc(id)}/down${qs(vars)}`, body).then((r) => r.job),
      /**
       * Take the compose project down and KEEP its volumes and axes. A request to any of its
       * hostnames brings it back (wake-on-call), as does `wake`. Refused (409) for `kind: shared`.
       */
      sleep: (id: string, vars?: Vars) => post<{ job: Job }>(`/api/deployments/${enc(id)}/sleep${qs(vars)}`).then((r) => r.job),
      /** `up`, recorded as a wake. */
      wake: (id: string, vars?: Vars) => post<{ job: Job }>(`/api/deployments/${enc(id)}/wake${qs(vars)}`).then((r) => r.job),
      /**
       * Mint a read-only link to this deployment: `views` default to both `details` and `logs`,
       * `ttl` to 7 days (30 at most). The token is returned once and stored nowhere; rotating
       * PSTACK_TOKEN is the only revocation.
       */
      share: (id: string, body: { views?: ShareView[]; ttl?: string } = {}) =>
        post<ShareLink>(`/api/deployments/${enc(id)}/share`, body),

      runtime: (id: string, vars?: Vars) => get<Runtime>(`/api/deployments/${enc(id)}/runtime${qs(vars)}`),

      /**
       * Is it SERVING yet — separate from "did the deploy job succeed", which only means the
       * commands ran. `wait` (seconds, max 60) long-polls and returns as soon as it is terminal.
       */
      readiness: (id: string, o: { wait?: number; refresh?: boolean; timeout?: number } & { vars?: Vars } = {}) =>
        get<Readiness & { id: string }>(
          `/api/deployments/${enc(id)}/readiness${qs(o.vars, { wait: o.wait, refresh: o.refresh ? 1 : undefined, timeout: o.timeout })}`,
        ),

      logs: (
        id: string,
        o: { tail?: number; service?: string; since?: string; timestamps?: boolean; vars?: Vars } = {},
      ) =>
        get<Logs>(
          `/api/deployments/${enc(id)}/logs${qs(o.vars, {
            tail: o.tail,
            service: o.service,
            since: o.since,
            timestamps: o.timestamps ? 1 : undefined,
          })}`,
        ),
    },

    /** One container, not the service and not the stack. The name must be one this deployment owns. */
    containers: {
      start: (id: string, name: string, vars?: Vars) =>
        post<{ container: string; action: string; note: string }>(`/api/deployments/${enc(id)}/containers/${enc(name)}/start${qs(vars)}`),
      stop: (id: string, name: string, o: { grace?: number; vars?: Vars } = {}) =>
        post<{ container: string; action: string; note: string }>(
          `/api/deployments/${enc(id)}/containers/${enc(name)}/stop${qs(o.vars, { grace: o.grace })}`,
        ),
      restart: (id: string, name: string, o: { grace?: number; vars?: Vars } = {}) =>
        post<{ container: string; action: string; note: string }>(
          `/api/deployments/${enc(id)}/containers/${enc(name)}/restart${qs(o.vars, { grace: o.grace })}`,
        ),
    },

    /** The swarm this daemon manages. `reachable: false` means docker did not answer — nothing else is known. */
    swarm: {
      info: () => get<SwarmInfo>('/api/swarm'),
      /**
       * What a new worker runs. A SECRET — whoever holds the token can add a node that runs any
       * task. `cloud-config` renders a user-data file; `distro` picks its Docker install steps.
       */
      join: (o: { format: 'token' | 'command' | 'script' | 'cloud-config'; distro?: string } = { format: 'command' }) =>
        get<string>(`/api/swarm/join${qs(undefined, { format: o.format, distro: o.distro })}`),
    },

    /**
     * The identity provider people sign in with. The sign-in flow itself is two browser redirects
     * (`/api/auth/sso/start` → the provider → `/api/auth/sso/callback`) and has nothing for an SDK
     * to call — this is the configuration around it.
     */
    sso: {
      /** Every stored provider (secrets never come back — `secretSet` is all a read learns). */
      config: () => get<SsoConfigResponse>('/api/sso/config'),
      /**
       * Save one provider under `key`, a lowercase slug. `key` may be omitted on a host with at
       * most one provider — an empty host derives it from the config, a one-provider host replaces
       * that one — which is what PUT meant before keys existed; with several it is a 400. Omit
       * `clientSecret` to keep that key's stored one. For `mode: 'oidc'` the issuer is fetched
       * here, so a bad one is a 400 now rather than a failed login later.
       */
      save: (config: Partial<SsoConfig> & { key?: string; clientSecret?: string }) =>
        put<{ ok: true; key: string; config: SsoConfig; callbackUrl: string }>('/api/sso/config', config),
      /**
       * Forget one provider — or the only one, when `key` is omitted (a 400 names the keys when
       * there are several). Nobody is deleted — accounts it created keep working.
       */
      remove: (key?: string) => del<{ ok: true }>(key ? `/api/sso/config/${enc(key)}` : '/api/sso/config'),
    },

    jobs: {
      list: () => get<{ jobs: Job[] }>('/api/jobs').then((r) => r.jobs),
      get: (jobId: string) => get<{ job: Job }>(`/api/jobs/${enc(jobId)}`).then((r) => r.job),
      /** Kills the command in flight. Undoes NOTHING — run `verify` afterwards. */
      cancel: (jobId: string) => post<{ cancelled: string; by: string; warning: string }>(`/api/jobs/${enc(jobId)}/cancel`),
    },

    specs: {
      list: () => get<{ specs: SpecMeta[] }>('/api/specs').then((r) => r.specs),
      get: (name: string) => get<SpecMeta & { spec?: string; compose?: string }>(`/api/specs/${enc(name)}`),
      put: (name: string, body: { spec: string; compose?: string; description?: string }) =>
        put<{ name: string; replaced: boolean }>(`/api/specs/${enc(name)}`, body),
      /** Refused while a deployment still references it. */
      remove: (name: string) => del<{ deleted: string }>(`/api/specs/${enc(name)}`),
    },

    notifiers: {
      list: () => get<{ notifiers: NotifierRow[] }>('/api/notifiers').then((r) => r.notifiers),
      /** The 201 is the ONLY time a signing secret exists in a response; `null` for types that do not sign. */
      create: (body: { name: string; type?: string; events: string[]; config: Record<string, unknown> }) =>
        post<{ notifier: NotifierRow; secret: string | null }>('/api/notifiers', body),
      setEnabled: (id: number, enabled: boolean) =>
        request<{ notifier: NotifierRow }>('PATCH', `/api/notifiers/${id}`, { enabled }),
      remove: (id: number) => del<{ deleted: number }>(`/api/notifiers/${id}`),
      test: (id: number) => post<{ ok: boolean }>(`/api/notifiers/${id}/test`),
      deliveries: (id: number) => get<{ deliveries: DeliveryRow[]; queued: number }>(`/api/notifiers/${id}/deliveries`),
      /** Replay a past delivery: same event id, fresh signature, `x-pstack-redelivery: 1`. */
      redeliver: (id: number, deliveryId: number) =>
        post<{ redelivered: number; event: string; eventId: string }>(`/api/notifiers/${id}/deliveries/${deliveryId}/redeliver`),
      /** Event names and per-type form fields — what a UI builds its pickers from. */
      meta: () => get<{ events: string[]; wildcard: string; types: Array<{ kind: string; label: string; signs: boolean }> }>('/api/notifiers/meta'),
    },

    hostVars: {
      list: () => get<{ entries: HostVar[] }>('/api/host-vars').then((r) => r.entries),
      /** `secret: true` is one-way — no route returns the value again. */
      put: (name: string, value: string, secret = false) =>
        put<{ name: string; secret: boolean }>(`/api/host-vars/${enc(name)}`, { value, secret }),
      remove: (name: string) => del<{ deleted: string }>(`/api/host-vars/${enc(name)}`),
    },

    /**
     * Poll a job until it stops running.
     *
     * Returns the finished job rather than throwing on a failed one: `failed`, `leaked` and
     * `cancelled` are ANSWERS, and a CI step usually wants to branch on which. It throws only when
     * the wait itself fails — a timeout, or the API being unreachable.
     */
    async waitForJob(jobId: string, o: { intervalMs?: number; timeoutMs?: number } = {}): Promise<Job> {
      const interval = o.intervalMs ?? 2_000;
      const deadline = Date.now() + (o.timeoutMs ?? 30 * 60_000);
      for (;;) {
        const job = await client.jobs.get(jobId);
        if (job.state !== 'running') return job;
        if (Date.now() > deadline) {
          throw new PstackError(0, jobId, { error: `job ${jobId} still running after the wait timeout` });
        }
        await new Promise((r) => setTimeout(r, interval));
      }
    },

    /**
     * Long-poll readiness until the stack settles.
     *
     * `watching` is not a failure and not an answer — it means "ask again", which is exactly the
     * loop people get wrong by hand. Returns on `ready` / `failed` / `timedout`; the caller decides
     * what each means for them.
     */
    async waitForReady(id: string, o: { vars?: Vars; timeoutMs?: number } = {}): Promise<Readiness> {
      const deadline = Date.now() + (o.timeoutMs ?? 10 * 60_000);
      for (;;) {
        const r = await client.deployments.readiness(id, { wait: 30, vars: o.vars });
        if (r.state !== 'watching') return r;
        if (Date.now() > deadline) {
          throw new PstackError(0, id, { error: `${id} was still converging after the wait timeout` });
        }
      }
    },
  };

  return client;
}

export type PstackClient = ReturnType<typeof createClient>;

/**
 * Verify a webhook this control plane sent you.
 *
 * The half of the integration that lives in the RECEIVER, which is why it ships here rather than
 * staying prose in the docs: the two mistakes it prevents are re-serialising the body before hashing
 * (the signature covers the exact bytes) and forgetting the staleness check (the timestamp is signed
 * precisely so a replayed request cannot claim a fresh one).
 *
 * `rawBody` must be the untouched request body — not a re-stringified object.
 */
export async function verifyWebhook(args: {
  secret: string;
  rawBody: string;
  headers: { get(name: string): string | null } | Record<string, string | undefined>;
  /** How old a delivery may be. Default 5 minutes; `0` disables the check. */
  toleranceMs?: number;
  now?: number;
}): Promise<{ ok: boolean; reason?: string; event?: string; redelivery: boolean }> {
  const read = (name: string): string | null => {
    const h = args.headers as { get?: (n: string) => string | null } & Record<string, string | undefined>;
    if (typeof h.get === 'function') return h.get(name);
    return h[name] ?? h[name.toLowerCase()] ?? null;
  };

  const signature = read('x-pstack-signature');
  const timestamp = read('x-pstack-timestamp');
  const redelivery = read('x-pstack-redelivery') === '1';
  if (!signature || !timestamp) return { ok: false, reason: 'missing signature headers', redelivery };

  const tolerance = args.toleranceMs ?? 5 * 60_000;
  if (tolerance > 0) {
    const age = Math.abs((args.now ?? Date.now()) - Number(timestamp));
    if (!Number.isFinite(age) || age > tolerance) return { ok: false, reason: 'stale timestamp', redelivery };
  }

  // WebCrypto rather than node:crypto — this file has to run in Node, Bun, Deno and a worker, and
  // `crypto.subtle` is the only one of the two that exists in all four.
  const key = await crypto.subtle.importKey(
    'raw',
    new TextEncoder().encode(args.secret),
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign'],
  );
  const mac = await crypto.subtle.sign('HMAC', key, new TextEncoder().encode(`${timestamp}.${args.rawBody}`));
  const expected = `sha256=${[...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, '0')).join('')}`;

  // Length-then-content, constant time over equal-length inputs. Not a hardened comparison — the
  // threat model is a wrong secret, not a remote timing oracle.
  if (signature.length !== expected.length) return { ok: false, reason: 'signature mismatch', redelivery };
  let diff = 0;
  for (let i = 0; i < expected.length; i++) diff |= signature.charCodeAt(i) ^ expected.charCodeAt(i);
  if (diff !== 0) return { ok: false, reason: 'signature mismatch', redelivery };

  return { ok: true, event: read('x-pstack-event') ?? undefined, redelivery };
}
