/**
 * Delivering domain events to the outside world.
 *
 * ── THE SEAM ─────────────────────────────────────────────────────────────────────────────────────
 *
 * A `NotifierType` is `{ kind, label, fields, validate, send }`. `TYPES` maps `kind` → type. Adding
 * Slack, Discord or email later is **one entry in that map**: a `validate` for its own config shape
 * and a `send` that formats its own body. No schema migration (the registration's `config` is opaque
 * JSON), no change to the bus, no change to any route, no change to the UI's plumbing — the UI reads
 * `fields` from `/api/notifiers/meta` and renders the form from it.
 *
 * Slack and Discord (below, built by `chatType`) ignore `secret` entirely, because for an
 * incoming-webhook URL the *URL* is the credential. That is the seam working, not a gap in it —
 * and it held: shipping both cost one factory, two registrations and zero schema or route changes.
 * The Slack payload shape is deliberately un-pinned to hooks.slack.com, so Mattermost and
 * Rocket.Chat incoming webhooks work with the same type.
 *
 * ── NOTHING AWAITS DELIVERY ──────────────────────────────────────────────────────────────────────
 *
 * `dispatch` is called from the bus, which is called from inside a job's `finally` and from request
 * handlers. It starts work and returns. A webhook endpoint that is down, slow, or hanging must not be
 * able to slow a deploy, let alone fail one — so every attempt carries an `AbortSignal.timeout`, and
 * `MAX_IN_FLIGHT` bounds concurrent sockets: this process also runs `docker`, and an event burst
 * across twenty notifiers would otherwise exhaust its file descriptors. Over the cap a delivery is
 * recorded as dropped rather than queued — a queue that grows during an outage is a disk leak wearing
 * a different hat.
 *
 * ── WHY THE URL IS CHECKED, AND WHAT THAT IS NOT ─────────────────────────────────────────────────
 *
 * The notifier URL is the one field that turns this control plane into an HTTP client aimed at an
 * address of someone else's choosing — including `169.254.169.254` (cloud metadata, i.e. instance
 * credentials) or `127.0.0.1:7878` (this very API). So loopback / link-local / private ranges are
 * refused, and a redirect is treated as a FAILURE rather than followed, because a public host that
 * 302s to the metadata endpoint defeats any registration-time check.
 *
 * **This is not a privilege boundary, and pretending otherwise would be dishonest.** Registering a
 * notifier requires auth, and anyone who can do that can already submit a spec — whose hooks are
 * `bash -c` on this host with the Docker socket mounted. They do not need SSRF. The check exists so a
 * *typo* aimed at an internal address fails loudly and visibly, and it is escapable with
 * `PSTACK_NOTIFY_ALLOW_PRIVATE=1` for the legitimate case of an internal collector on the same box.
 */

import { mask, redactText } from './redact.ts';
import type { PstackEvent } from './events.ts';
import type { NotifierRow, Webhooks } from './webhooks.ts';

export class NotifierError extends Error {}

/** Per attempt. Long enough for a cold lambda, short enough that three attempts stay bounded. */
const ATTEMPT_TIMEOUT_MS = 5_000;
/** Attempt 1 is immediate; these are the waits BEFORE attempts 2 and 3. Worst case ~21s. */
const RETRY_DELAYS_MS = [1_000, 5_000];
/** Concurrent in-flight deliveries across all notifiers. */
const MAX_IN_FLIGHT = 8;
/**
 * Events allowed to wait for ONE notifier before the oldest is dropped.
 *
 * Sized for a burst, not for an outage: a deploy emits under a dozen events, so this absorbs several
 * deploys' worth back to back while a slow receiver catches up. A receiver that is down long enough
 * to exceed it is not going to be saved by a bigger number — that is what redelivery is for.
 */
const MAX_QUEUED_PER_NOTIFIER = 200;

export type DeliveryResult = {
  ok: boolean;
  status?: number;
  error?: string;
};

/** A form field the UI renders for this type's `config`. */
export type NotifierField = {
  key: string;
  label: string;
  placeholder?: string;
  required: boolean;
  /**
   * This field is CREDENTIAL MATERIAL and must never be returned by a read path.
   *
   * The seam's own example is the reason this exists: a Slack incoming-webhook URL *is* the
   * credential — anyone holding it can post as the app — so `{ webhookUrl }` is a secret in a way
   * that a plain webhook's `{ url }` is not. Without a per-field declaration the only options were
   * to return every config verbatim (leaking the Slack case) or mask all of them (hiding the
   * webhook URL an operator registered the notifier to check). The type knows; nothing else can.
   */
  secret?: boolean;
};

export type NotifierType = {
  kind: string;
  label: string;
  fields: NotifierField[];
  /**
   * Does this type use the HMAC signing secret?
   *
   * `false` for anything whose config already carries its credential — Slack and Discord, the two
   * this seam was designed for. Without it the secret was unconditional across four layers: the
   * column, `create()`, the 201 body, and a UI banner reading "your receiver needs it to verify the
   * X-Pstack-Signature header". Register a Slack notifier and an operator would be handed 48 hex
   * characters, told it was the only time they would see them, and given no way to ever use them.
   */
  signs: boolean;
  /** Throw `NotifierError` with a message naming the fix. Called before anything is stored. */
  validate: (config: Record<string, unknown>) => void;
  /**
   * Deliver one event. CONTRACT: never throws — return `{ ok: false, error }` instead. The dispatcher
   * defends against a breach of that contract anyway, because a third-party type getting it wrong
   * must not take down the dispatcher.
   *
   * `e.data.test === true` marks a TEST delivery from `POST /api/notifiers/:id/test`. A type that
   * renders per event — the Slack card this seam anticipates — must check it, because the synthesized
   * event name is a real one and would otherwise draw a green "Job succeeded" nobody's job earned.
   */
  send: (
    e: PstackEvent,
    config: Record<string, unknown>,
    secret: string,
    signal: AbortSignal,
  ) => Promise<DeliveryResult>;
};

// ── URL safety ──────────────────────────────────────────────────────────────────────────────────

/**
 * Is this host one the control plane should refuse to call?
 *
 * Written as a classifier rather than one regexp because the regexp version was wrong in BOTH
 * directions, and each direction is its own kind of bad:
 *
 *   - **False positives.** A `fc|fd|fe80` prefix alternative matches any DNS NAME starting with
 *     those letters — `fcm.googleapis.com` (Firebase, an entirely plausible webhook target) was
 *     refused as if it were an IPv6 unique-local address.
 *   - **False negatives.** `URL.hostname` KEEPS the brackets on an IPv6 literal, so `http://[fc00::1]/`
 *     has hostname `[fc00::1]` — which starts with `[`, so the same prefix test never fired and the
 *     genuinely private address sailed through.
 *
 * So: decide what kind of host it is first, then apply the rule for that kind. A DNS name is never
 * tested against IP rules, and an IP literal is never tested against name rules.
 */
function isPrivateHost(hostname: string): boolean {
  // Trailing dot is a legal FQDN form and must not defeat a name comparison.
  const host = hostname.toLowerCase().replace(/\.$/, '');

  // ── IPv6 literal: bracketed in a URL, so unwrap before parsing.
  if (host.startsWith('[') && host.endsWith(']')) {
    const v6 = host.slice(1, -1).split('%')[0]!; // strip a zone id (fe80::1%eth0)
    if (v6 === '::1' || v6 === '::') return true;
    // fc00::/7 (unique local) — first byte fc or fd. fe80::/10 (link local).
    if (/^f[cd][0-9a-f]{0,2}:/.test(v6) || /^fe[89ab][0-9a-f]?:/.test(v6)) return true;
    // IPv4-mapped. `URL.hostname` NORMALIZES `::ffff:127.0.0.1` to the hex form `::ffff:7f00:1`, so
    // matching only the dotted spelling would miss every one the parser actually hands us.
    const dotted = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/.exec(v6);
    if (dotted) return isPrivateHost(dotted[1]!);
    const hex = /^::ffff:([0-9a-f]{1,4}):([0-9a-f]{1,4})$/.exec(v6);
    if (hex) {
      const [hi, lo] = [parseInt(hex[1]!, 16), parseInt(hex[2]!, 16)];
      return isPrivateHost(`${hi >> 8}.${hi & 255}.${lo >> 8}.${lo & 255}`);
    }
    return false;
  }

  // ── IPv4 literal: parse octets. A prefix string match cannot express 172.16.0.0/12.
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (v4) {
    const [a, b] = [Number(v4[1]), Number(v4[2])];
    if (a === 0 || a === 127) return true; // this host
    if (a === 10) return true; // private
    if (a === 172 && b >= 16 && b <= 31) return true; // 172.16.0.0/12 — NOT all of 172.x
    if (a === 192 && b === 168) return true; // private
    if (a === 169 && b === 254) return true; // link-local, incl. cloud metadata
    return false;
  }

  // ── A DNS name. Only exact names, never prefixes — `fcm.googleapis.com` is not `fc00::`.
  return host === 'localhost' || host.endsWith('.localhost');
}

export { isPrivateHost };

export function assertDeliverableUrl(raw: string): URL {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new NotifierError(`"${raw}" is not a URL`);
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new NotifierError(`only http and https are deliverable, got "${url.protocol}"`);
  }
  if (process.env.PSTACK_NOTIFY_ALLOW_PRIVATE === '1') return url;
  if (isPrivateHost(url.hostname)) {
    throw new NotifierError(
      `"${url.hostname}" is a loopback, link-local or private address. The control plane would be ` +
        `making the request, so this can reach services only it can see — including cloud metadata. ` +
        `Set PSTACK_NOTIFY_ALLOW_PRIVATE=1 if that is genuinely what you want.`,
    );
  }
  return url;
}

// ── the webhook type ────────────────────────────────────────────────────────────────────────────

/**
 * The four fields a receiver gets, and the exact object stored for redelivery.
 *
 * One function so the two can never disagree: a stored "payload" that is not byte-identical to what
 * was sent is a replay of something that never happened.
 */
export function envelope(e: PstackEvent): { id: string; event: string; at: number; data: Record<string, unknown> } {
  return { id: e.id, event: e.event, at: e.at, data: e.data };
}

function sign(secret: string, timestamp: number, body: string): string {
  // Bun's builtin, keyed — byte-identical to node:crypto's createHmac, and this package carries zero
  // runtime dependencies.
  return `sha256=${new Bun.CryptoHasher('sha256', secret).update(`${timestamp}.${body}`).digest('hex')}`;
}

export const webhookType: NotifierType = {
  kind: 'webhook',
  // The URL is an address, not a credential — the HMAC secret is what proves authenticity, so the
  // URL stays readable in the list view where it is the only thing identifying the notifier.
  signs: true,
  label: 'HTTP webhook',
  fields: [{ key: 'url', label: 'URL', placeholder: 'https://example.com/hooks/pstack', required: true }],
  validate(config) {
    if (typeof config.url !== 'string' || !config.url.trim()) {
      throw new NotifierError('a webhook needs a `url`');
    }
    assertDeliverableUrl(config.url);
  },
  async send(e, config, secret, signal) {
    const url = String(config.url);
    // SERIALISED EXACTLY ONCE. Signing one string and sending a separately-stringified object is how
    // signatures mysteriously fail against a receiver doing everything right.
    const body = JSON.stringify(envelope(e));
    try {
      const r = await fetch(url, {
        method: 'POST',
        // A 3xx is a FAILURE, not a hop to follow: a public host that redirects to the metadata
        // endpoint is exactly how a registration-time address check gets defeated.
        redirect: 'manual',
        headers: {
          'content-type': 'application/json',
          'user-agent': 'pstack',
          'x-pstack-event': e.event,
          // Stable across all attempts AND across the notifiers of one event — the receiver's
          // dedupe key. Minting a fresh id per retry would make at-least-once undedupable.
          'x-pstack-delivery': e.id,
          'x-pstack-timestamp': String(e.at),
          'x-pstack-signature': sign(secret, e.at, body),
          // A REPLAY of an event this receiver was already sent. It carries the original `id`, so a
          // receiver that deduped it the first time still will; this header is how one that did NOT
          // record it can tell the difference between a replay and a new event.
          ...(e.replay ? { 'x-pstack-redelivery': '1' } : {}),
        },
        body,
        signal,
      });
      if (r.status >= 300 && r.status < 400) {
        return { ok: false, status: r.status, error: `redirect to ${r.headers.get('location') ?? '?'} not followed` };
      }
      return { ok: r.ok, status: r.status, error: r.ok ? undefined : `HTTP ${r.status}` };
    } catch (err) {
      /*
       * Includes the abort. MEASURED on Bun 1.3: a connection failure here is
       * "Unable to connect. Is the computer able to access the url?" and does NOT carry the URL —
       * so the redaction the caller applies is currently prospective, not load-bearing for THIS
       * type. It stays because the message is a third party's to choose: another runtime words it
       * with the URL, and `send` is a seam any future type implements however it likes. Do not
       * remove it on the grounds that today's message happens to be clean.
       */
      return { ok: false, error: (err as Error).message };
    }
  },
};

/**
 * One human-readable line per event, shared by every chat-shaped type.
 *
 * Chat messages are READ, not parsed — a Slack channel getting raw JSON is a notifier nobody keeps
 * enabled. One formatter, not one per type, so Slack and Discord can never describe the same event
 * differently; each type only decides the envelope it posts the line in.
 *
 * `data.test === true` is the contract flag from `POST /:id/test` (see NotifierType.send): the
 * synthesized event name is a real one, and without this check a test would render as a green
 * "Deploy succeeded" nobody's deploy earned.
 */
export function summarize(e: PstackEvent): string {
  if (e.data.test === true) {
    return 'Test delivery from pstack — the connection works. No job ran.';
  }
  const d = e.data as Record<string, unknown>;
  const stack = typeof d.stack === 'string' ? d.stack : String(d.id ?? '');
  const took =
    typeof d.durationMs === 'number' ? ` in ${Math.max(1, Math.round(d.durationMs / 1000))}s` : '';
  switch (e.event) {
    case 'deployment.created':
      return `Deployment ${d.id} created (stack ${stack}).`;
    case 'deployment.updated':
      return `Deployment ${d.id} updated (stack ${stack}).`;
    case 'deployment.deleted':
      return `Deployment ${d.id} forgotten. Nothing was torn down.`;
    case 'job.started':
      return `${actionWord(d.action)} started on ${stack}.`;
    case 'job.succeeded':
      return `${actionWord(d.action)} succeeded on ${stack}${took}.`;
    case 'job.cancelled':
      return `${actionWord(d.action)} on ${stack} was CANCELLED${d.cancelledBy ? ` by ${String(d.cancelledBy)}` : ''}${took}. Nothing was undone — verify what exists.`;
    case 'job.failed':
      return `${actionWord(d.action)} FAILED on ${stack}${took}.${d.error ? ` ${String(d.error)}` : ''}`;
    case 'job.leaked': {
      const axes = Array.isArray(d.leakedAxes) && d.leakedAxes.length ? ` Leaked: ${(d.leakedAxes as string[]).join(', ')}.` : '';
      // The event this product exists for — the wording must be impossible to skim past as routine.
      return `Teardown LEAKED on ${stack} — resources survived and nothing will retry.${axes}`;
    }
    case 'spec.stored':
      return `Spec ${d.name} ${d.replaced ? 'replaced' : 'stored'}.`;
    case 'spec.deleted':
      return `Spec ${d.name} deleted.`;
    case 'routing.changed':
      return `Routing file ${d.file} ${d.action}.`;
    // Readiness. Deliberately NOT one line per healthcheck transition in chat — a `starting →
    // healthy` for every container would bury the verdict it leads up to. The per-container and
    // per-probe events still deliver (someone subscribed to them asked for them), but the wording
    // stays proportionate: the stack-level line is the one that reads like news.
    case 'healthcheck.started':
      return `${d.container} healthcheck ${d.status} on ${stack}.`;
    case 'healthcheck.updated':
      return `${d.container} healthcheck ${d.previous} → ${d.status} on ${stack}.`;
    case 'healthcheck.finished':
      return `${d.container} healthcheck ${d.healthy === true ? 'passed' : 'FAILED'} on ${stack}.`;
    case 'healthcheck.timedout':
      return `${d.container} healthcheck never settled on ${stack} — still starting after ${Math.round(Number(d.waitedMs ?? 0) / 1000)}s.`;
    case 'container.started':
      return `${d.container} was started on ${stack}${d.by ? ` by ${String(d.by)}` : ''}.`;
    case 'container.stopped':
      return `${d.container} was STOPPED on ${stack}${d.by ? ` by ${String(d.by)}` : ''} — it stays stopped until something starts it.`;
    case 'container.restarted':
      return `${d.container} was restarted on ${stack}${d.by ? ` by ${String(d.by)}` : ''}.`;
    case 'container.ready':
      return `${d.container} is ready on ${stack}${d.hasHealthcheck === true ? ' (healthcheck passed)' : ' (running; no healthcheck to check)'}.`;
    case 'container.start-failed':
      return `${d.container} failed to start on ${stack}${d.reason ? ` — ${String(d.reason)}` : ''}.`;
    case 'stack.ready':
      return `${stack} is ready — ${d.ready}/${d.containers} container(s) up${took}.`;
    case 'stack.failed': {
      const which = Array.isArray(d.failedContainers) && d.failedContainers.length
        ? ` Failed: ${(d.failedContainers as string[]).join(', ')}.`
        : '';
      return `${stack} did not come up${took}.${which}`;
    }
    case 'stack.timedout': {
      const which = Array.isArray(d.pendingContainers) && d.pendingContainers.length
        ? ` Still not ready: ${(d.pendingContainers as string[]).join(', ')}.`
        : '';
      return `${stack} did not become ready in time${took}.${which}`;
    }
    default:
      return `${e.event} on ${stack || 'this host'}.`;
  }
}

function actionWord(action: unknown): string {
  return action === 'up' ? 'Deploy' : action === 'down' ? 'Teardown' : action === 'verify' ? 'Verify' : String(action);
}

/**
 * A chat-webhook type: the whole difference between Slack and Discord is the URL's owner and the
 * JSON key the text goes under, so one factory builds both. `signs: false` and `secret: true` on
 * the URL field are the pair that makes the 0.14.0 seam work: no HMAC secret is minted or shown
 * (for an incoming webhook the URL IS the credential), and every read path masks it.
 */
function chatType(args: {
  kind: string;
  label: string;
  placeholder: string;
  /** `{ [textKey]: line }` — "text" for Slack-compatible receivers, "content" for Discord. */
  textKey: string;
}): NotifierType {
  return {
    kind: args.kind,
    label: args.label,
    signs: false,
    fields: [
      {
        key: 'webhookUrl',
        label: 'Incoming webhook URL',
        placeholder: args.placeholder,
        required: true,
        // Anyone holding this URL can post as the app — it is the credential, not an address.
        secret: true,
      },
    ],
    validate(config) {
      if (typeof config.webhookUrl !== 'string' || !config.webhookUrl.trim()) {
        throw new NotifierError(`a ${args.label} notifier needs a \`webhookUrl\``);
      }
      assertDeliverableUrl(config.webhookUrl);
    },
    async send(e, config, _secret, signal) {
      try {
        const r = await fetch(String(config.webhookUrl), {
          method: 'POST',
          redirect: 'manual',
          headers: { 'content-type': 'application/json', 'user-agent': 'pstack' },
          body: JSON.stringify({ [args.textKey]: summarize(e) }),
          signal,
        });
        if (r.status >= 300 && r.status < 400) {
          return { ok: false, status: r.status, error: `redirect to ${r.headers.get('location') ?? '?'} not followed` };
        }
        // Discord answers 204 on success; Slack answers 200 "ok". `r.ok` covers both.
        return { ok: r.ok, status: r.status, error: r.ok ? undefined : `HTTP ${r.status}` };
      } catch (err) {
        return { ok: false, error: (err as Error).message };
      }
    },
  };
}

/** The host is deliberately NOT pinned to hooks.slack.com: Mattermost and Rocket.Chat speak the
 *  same `{ text }` payload on their own domains, and pinning would lock them out for no safety —
 *  the URL is already checked by `assertDeliverableUrl`. */
export const slackType = chatType({
  kind: 'slack',
  label: 'Slack',
  placeholder: 'https://hooks.slack.com/services/T…/B…/…',
  textKey: 'text',
});

export const discordType = chatType({
  kind: 'discord',
  label: 'Discord',
  placeholder: 'https://discord.com/api/webhooks/…/…',
  textKey: 'content',
});

/**
 * `null` prototype, deliberately.
 *
 * A plain object literal inherits from `Object.prototype`, so `TYPES['constructor']` is a truthy
 * FUNCTION and `TYPES['__proto__']` is a truthy object — a caller registering `type: "constructor"`
 * sailed past the unknown-type guard and got `Object.prototype.constructor` back, which then failed
 * far from the cause with a confusing shape error. `Object.create(null)` makes the map contain only
 * what was put in it.
 */
export const TYPES: Record<string, NotifierType> = Object.assign(Object.create(null) as Record<string, NotifierType>, {
  [webhookType.kind]: webhookType,
  [slackType.kind]: slackType,
  [discordType.kind]: discordType,
});

export function typeOf(kind: string): NotifierType {
  // `Object.hasOwn` as well as the null prototype: belt and braces, and it states the intent for a
  // reader who does not know why the map is built the way it is.
  const t = Object.hasOwn(TYPES, kind) ? TYPES[kind] : undefined;
  if (!t) {
    throw new NotifierError(
      `unknown notifier type "${kind}" — known types: ${Object.keys(TYPES).join(', ')}`,
    );
  }
  return t;
}

/**
 * A type's config, safe to hand back to a caller.
 *
 * Fields the type marked `secret` become a mask. This is what makes `webhooks.ts`'s stated property
 * — "no function in this module returns a credential" — survive the very next type the design
 * names, rather than being true only of the one column it was written about.
 */
export function publicConfig(kind: string, config: Record<string, unknown>): Record<string, unknown> {
  const type = Object.hasOwn(TYPES, kind) ? TYPES[kind] : undefined;
  if (!type) return config;
  const secretKeys = new Set(type.fields.filter((f) => f.secret).map((f) => f.key));
  if (secretKeys.size === 0) return config;
  return Object.fromEntries(
    Object.entries(config).map(([k, v]) => [k, secretKeys.has(k) && typeof v === 'string' ? mask(v) : v]),
  );
}

/** Redact a message the way the dispatcher does — for the one send path that is not the dispatcher. */
export function redactForNotifier(
  message: string,
  secret: string,
  config: Record<string, unknown>,
): string {
  return redactText(message, [secret, ...Object.values(config).filter((v): v is string => typeof v === 'string')]);
}

/** For `POST /api/notifiers` — validates without this module knowing about the store. */
export function validateConfig(kind: string, config: Record<string, unknown>): void {
  typeOf(kind).validate(config);
}

// ── the dispatcher ──────────────────────────────────────────────────────────────────────────────

export class Dispatcher {
  #hooks: Webhooks;
  #inFlight = 0;
  /**
   * Notifier ids with a delivery in progress. At most ONE delivery per notifier runs at a time.
   *
   * Without this, a single black-holing endpoint takes the whole process down to its own speed: a
   * delivery costs up to 21s (3 attempts × 5s timeout, plus 1s and 5s backoff), so eight events
   * inside that window put all eight slots on ONE broken notifier — and every healthy notifier's
   * event is then written straight to the log as "dropped". A broken notifier must degrade itself,
   * not everyone else.
   */
  #busy = new Set<number>();
  /**
   * PENDING EVENTS PER NOTIFIER — the fix for "dropped, a delivery is still in progress".
   *
   * That serialization is right, but the original response to it — drop the event and record the
   * drop — was not. It made a burst unsurvivable: readiness alone emits `healthcheck.started`,
   * `updated`, `finished`, `container.ready` and `stack.ready` within seconds of one deploy, while a
   * single delivery can take 21s (3 attempts × 5s timeout plus backoff). Everything after the first
   * event was written straight to the log as dropped, and nothing ever retried it — a notifier that
   * worked perfectly reported four failures per deploy.
   *
   * So events QUEUE instead, and the queue is BOUNDED. The original objection to a queue was that
   * one growing through an outage is a disk leak wearing a different hat; a hard cap answers that
   * without throwing away the ordinary case. Past the cap the OLDEST is dropped and recorded as
   * such — under sustained overload the recent events are the ones worth having, and the drop is
   * still visible rather than silent.
   */
  #queues = new Map<number, Array<{ row: NotifierRow; event: PstackEvent }>>();

  constructor(hooks: Webhooks) {
    this.#hooks = hooks;
  }

  /** How many events are waiting, per notifier — so a UI can say "40 queued" instead of "nothing yet". */
  queued(notifierId: number): number {
    return this.#queues.get(notifierId)?.length ?? 0;
  }

  /** Bus listener. Returns immediately; every failure path is contained. */
  dispatch(e: PstackEvent): void {
    let rows: NotifierRow[];
    try {
      rows = this.#hooks.subscribedTo(e.event);
    } catch {
      // A database that cannot be read must not break the operation that emitted.
      return;
    }
    for (const row of rows) this.#enqueue(row, e);
    this.#pump();
  }

  /**
   * Send this exact event to this notifier again, as a REPLAY.
   *
   * Queued like any other delivery — a redelivery of a hundred events must not be a hundred
   * simultaneous sockets — and marked, so the receiver can tell a replay from the original.
   */
  redeliver(row: NotifierRow, e: PstackEvent): void {
    this.#enqueue(row, e, true);
    this.#pump();
  }

  #enqueue(row: NotifierRow, event: PstackEvent, replay = false): void {
    const q = this.#queues.get(row.id) ?? [];
    if (q.length >= MAX_QUEUED_PER_NOTIFIER) {
      const oldest = q.shift();
      if (oldest) {
        this.#drop(
          oldest.row,
          oldest.event,
          `dropped — ${MAX_QUEUED_PER_NOTIFIER} events already waiting for this notifier, and it is not keeping up`,
        );
      }
    }
    q.push({ row, event: replay ? { ...event, replay: true } : event });
    this.#queues.set(row.id, q);
  }

  /**
   * Start whatever can start now.
   *
   * Called after every enqueue and again as each delivery finishes, so a freed slot is used
   * immediately rather than waiting for the next event to arrive and notice.
   */
  #pump(): void {
    for (const [id, q] of this.#queues) {
      if (q.length === 0) {
        this.#queues.delete(id);
        continue;
      }
      if (this.#busy.has(id)) continue;
      if (this.#inFlight >= MAX_IN_FLIGHT) return; // globally saturated; the next `finally` re-pumps
      const next = q.shift()!;
      if (q.length === 0) this.#queues.delete(id);
      this.#inFlight++;
      this.#busy.add(id);
      void this.#deliver(next.row, next.event)
        .catch(() => {
          // Last resort. A notifier deleted mid-delivery makes the delivery INSERT violate its
          // foreign key, and with `PRAGMA foreign_keys = ON` that throws inside a voided promise —
          // an unhandled rejection discovered in production months later.
        })
        .finally(() => {
          this.#inFlight--;
          this.#busy.delete(id);
          this.#pump();
        });
    }
  }

  /** Record a non-attempt. Silence here would be the exact failure this product exists to remove. */
  #drop(row: NotifierRow, e: PstackEvent, reason: string): void {
    try {
      const id = this.#hooks.startDelivery(row.id, e.id, e.event);
      this.#hooks.finishDelivery(id, { status: 'failed', attempts: 0, error: reason });
    } catch {
      /* the row may have been deleted mid-dispatch */
    }
  }

  async #deliver(row: NotifierRow, e: PstackEvent): Promise<void> {
    const type = Object.hasOwn(TYPES, row.type) ? TYPES[row.type] : undefined;
    if (!type) return;
    const secret = this.#hooks.secretOf(row.id);
    if (secret === null) return; // deleted between the read and here

    const deliveryId = this.#hooks.startDelivery(row.id, e.id, e.event, envelope(e));
    let last: DeliveryResult = { ok: false, error: 'not attempted' };
    let attempt = 0;

    for (;;) {
      attempt++;
      last = await type
        .send(e, row.config, secret, AbortSignal.timeout(ATTEMPT_TIMEOUT_MS))
        .catch((err) => ({ ok: false, error: (err as Error).message }) as DeliveryResult);
      const wait = RETRY_DELAYS_MS[attempt - 1];
      if (last.ok || wait === undefined) break;
      await Bun.sleep(wait);
      // DELETE returns 200 immediately; without this the endpoint the operator just unregistered
      // still receives two more signed POSTs over the following 6 seconds.
      if (this.#hooks.secretOf(row.id) === null) {
        last = { ...last, error: 'notifier deleted before retry' };
        break;
      }
    }

    this.#hooks.finishDelivery(deliveryId, {
      status: last.ok ? 'ok' : 'failed',
      attempts: attempt,
      responseCode: last.status,
      error: last.error ? this.#redact(row, secret, last.error) : undefined,
    });
    this.#hooks.noteResult(row.id, last.ok ? 'ok' : 'failed');
    this.#hooks.prune(row.id);
  }

  /**
   * Strip credentials from a stored error.
   *
   * A fetch failure message contains the URL — and for a Slack or Discord incoming webhook the URL
   * *is* the credential. So every string in `config` is redacted, not just the signing secret: a
   * future type's SMTP password is then covered by an author who never read this comment.
   * `redactText` ignores strings under 8 characters, so a `channel: '#ops'` survives intact.
   */
  #redact(row: NotifierRow, secret: string, message: string): string {
    return redactForNotifier(message, secret, row.config);
  }
}
