/**
 * Notifier registrations and their delivery log.
 *
 * ── THE COMPOSABILITY SEAM IS THE `type` COLUMN ──────────────────────────────────────────────────
 *
 * A registration is `{ type, name, events[], config{} }`. `type` selects a delivery module (see
 * `notify.ts`) and OWNS the shape of `config` — `{url}` for a webhook, `{webhookUrl, channel}` for
 * Slack, `{to, from}` for email. Adding Slack later is a new entry in the notifier registry and a
 * new `config` validator; it is **not** a migration, not a schema change, and not a new table. That
 * is the whole point of storing `config` as opaque JSON here and validating it in the module that
 * understands it.
 *
 * ── THE SIGNING SECRET IS STORED REVERSIBLY, DELIBERATELY ────────────────────────────────────────
 *
 * Sessions and personal tokens in `auth.ts` are SHA-256'd because they are INBOUND credentials —
 * the server only ever verifies what a caller presents, so one-way is strictly better. A signing
 * secret is the opposite: this process must compute `HMAC(secret, body)` on every OUTBOUND delivery,
 * so it needs the plaintext. Hashing it would make signing impossible.
 *
 * The precedent is therefore `registries.ts`, not `auth.ts`: reversible at rest, protected by the
 * 0700 directory and 0600 database file, and — the part enforced here — **no function in this module
 * returns it**. `list()` and `get()` project columns explicitly and omit `secret`; only the private
 * `secretOf()` reaches it, and only `notify.ts` calls it. A test asserts it appears in no response.
 *
 * ── WHY DELIVERIES ARE PERSISTED AND BOUNDED ─────────────────────────────────────────────────────
 *
 * `JobRegistry` keeps the last 50 jobs IN MEMORY and evicts, so a retry that re-read a job by id
 * minutes later could find it gone. Everything a delivery needs is therefore captured at emit time
 * and written here. And a delivery row per event per notifier on a busy host is unbounded growth on
 * the same disk as the registry, so `prune()` caps it — a log nobody bounded is a disk leak with a
 * later ticket.
 */

import type { Store } from './store.ts';
import { EVENTS, WILDCARD, isSubscribable, type EventName } from './events.ts';

export class WebhookError extends Error {}

/** Keep this many delivery rows per notifier. Enough to debug a failing endpoint, bounded on disk. */
const MAX_DELIVERIES_PER_NOTIFIER = 200;

const NAME = /^[\w][\w .:@/-]{0,63}$/;

/** What a caller may see. Note the absence of `secret` — that is the security property, in the type. */
export type NotifierRow = {
  id: number;
  type: string;
  name: string;
  config: Record<string, unknown>;
  /** Event names, or the single entry `'*'` meaning everything including future events. */
  events: (EventName | '*')[];
  enabled: boolean;
  createdAt: number;
  lastStatus: string | null;
  lastAt: number | null;
};

export type DeliveryRow = {
  id: number;
  notifierId: number;
  eventId: string;
  event: string;
  status: string;
  attempts: number;
  responseCode: number | null;
  error: string | null;
  createdAt: number;
  updatedAt: number;
};

type RawNotifier = {
  id: number;
  type: string;
  name: string;
  config: string;
  events: string;
  enabled: number;
  created_at: number;
  last_status: string | null;
  last_at: number | null;
};

const toRow = (r: RawNotifier): NotifierRow => ({
  id: r.id,
  type: r.type,
  name: r.name,
  config: JSON.parse(r.config) as Record<string, unknown>,
  events: JSON.parse(r.events) as (EventName | '*')[],
  enabled: r.enabled === 1,
  createdAt: r.created_at,
  lastStatus: r.last_status,
  lastAt: r.last_at,
});

/** Columns a caller may read. Written out rather than `SELECT *` so adding a secret-bearing column
 *  later cannot silently start leaking it through every existing read path. */
const PUBLIC_COLUMNS =
  'id, type, name, config, events, enabled, created_at, last_status, last_at';

export class Webhooks {
  #store: Store;

  constructor(store: Store, publicConfig?: (kind: string, c: Record<string, unknown>) => Record<string, unknown>) {
    this.#store = store;
    this.#public = publicConfig ?? ((_k, c) => c);
  }

  /**
   * Every row, with credential-bearing config fields masked.
   *
   * `subscribedTo` deliberately does NOT go through this — the dispatcher needs the real values to
   * make the request. Read paths and the delivery path want different things from the same row, and
   * conflating them is how a masked value ends up being POSTed to a masked URL.
   */
  list(): NotifierRow[] {
    return this.#raw().map((r) => ({ ...r, config: this.#public(r.type, r.config) }));
  }

  get(id: number): NotifierRow | null {
    const r = this.#store.db
      .query(`SELECT ${PUBLIC_COLUMNS} FROM notifiers WHERE id = ?`)
      .get(id) as RawNotifier | null;
    if (!r) return null;
    const row = toRow(r);
    return { ...row, config: this.#public(row.type, row.config) };
  }

  #raw(): NotifierRow[] {
    return (
      this.#store.db
        .query(`SELECT ${PUBLIC_COLUMNS} FROM notifiers ORDER BY created_at DESC`)
        .all() as RawNotifier[]
    ).map(toRow);
  }

  /** Injected rather than imported, so this module still knows nothing about what a webhook is. */
  #public: (kind: string, config: Record<string, unknown>) => Record<string, unknown>;

  /** Every enabled notifier subscribed to this event. The dispatcher's only read path. */
  subscribedTo(event: EventName): NotifierRow[] {
    // Filtered in JS rather than with a LIKE on the JSON: a LIKE would match 'job.failed' inside
    // 'job.failed_x' if an event were ever named that way, and the list is tiny.
    return this.#raw().filter(
      (n) => n.enabled && (n.events.includes(event) || (n.events as string[]).includes(WILDCARD)),
    );
  }

  /**
   * The signing secret, for `notify.ts` only.
   *
   * Named to be conspicuous in a diff. If a second caller ever appears, that is the moment to ask
   * whether it should exist — every other path in this module deliberately cannot reach this column.
   */
  secretOf(id: number): string | null {
    const r = this.#store.db.query('SELECT secret FROM notifiers WHERE id = ?').get(id) as
      | { secret: string }
      | null;
    return r?.secret ?? null;
  }

  /**
   * Register a notifier. Returns the row AND the generated signing secret — the only time the
   * secret leaves this module toward a caller, mirroring how a personal token is shown once.
   *
   * `validateConfig` is supplied by the notifier registry so this class never learns what a Slack
   * config looks like. That indirection is the composability seam doing its job.
   */
  create(args: {
    type: string;
    name: string;
    config: Record<string, unknown>;
    events: string[];
    validateConfig: (type: string, config: Record<string, unknown>) => void;
    /** Whether this type signs. A type whose config already carries its credential gets no secret. */
    signs: boolean;
  }): { row: NotifierRow; secret: string | null } {
    if (!NAME.test(args.name)) {
      throw new WebhookError(
        `name must be 1–64 characters of letters, digits, space, or . : @ / _ -  (got "${args.name}")`,
      );
    }
    if (!Array.isArray(args.events) || args.events.length === 0) {
      throw new WebhookError(
        `subscribe to at least one event. Known events: ${EVENTS.join(', ')}`,
      );
    }
    // Refused at write time, not discovered as silence months later — the same reasoning as
    // refusing an unknown compose profile.
    const unknown = args.events.filter((e) => !isSubscribable(e));
    if (unknown.length) {
      throw new WebhookError(
        `unknown event(s): ${unknown.join(', ')}. Known events: ${EVENTS.join(', ')} (or "${WILDCARD}" for all)`,
      );
    }
    args.validateConfig(args.type, args.config);

    /*
     * Empty for a non-signing type, and the column stays NOT NULL. Minting one anyway and simply
     * not showing it would leave a live credential in the database that nothing can ever use or
     * rotate — a secret that exists only to be forgotten is still a secret to be stolen.
     */
    const secret = args.signs
      ? `whsec_${Buffer.from(crypto.getRandomValues(new Uint8Array(24))).toString('hex')}`
      : '';
    const r = this.#store.db
      .query(
        `INSERT INTO notifiers (type, name, config, events, secret, created_at)
         VALUES (?, ?, ?, ?, ?, ?) RETURNING ${PUBLIC_COLUMNS}`,
      )
      .get(
        args.type,
        args.name,
        JSON.stringify(args.config),
        JSON.stringify([...new Set(args.events)]),
        secret,
        Date.now(),
      ) as RawNotifier;
    return { row: toRow(r), secret: args.signs ? secret : null };
  }

  setEnabled(id: number, enabled: boolean): boolean {
    return (
      this.#store.db
        .query('UPDATE notifiers SET enabled = ? WHERE id = ?')
        .run(enabled ? 1 : 0, id).changes > 0
    );
  }

  remove(id: number): boolean {
    // Deliveries cascade (ON DELETE CASCADE).
    return this.#store.db.query('DELETE FROM notifiers WHERE id = ?').run(id).changes > 0;
  }

  // ── deliveries ────────────────────────────────────────────────────────────────────────────────

  /** Open a delivery row. Everything needed is captured NOW — the source job may be evicted later. */
  startDelivery(notifierId: number, eventId: string, event: string): number {
    const now = Date.now();
    const r = this.#store.db
      .query(
        `INSERT INTO deliveries (notifier_id, event_id, event, status, created_at, updated_at)
         VALUES (?, ?, ?, 'pending', ?, ?) RETURNING id`,
      )
      .get(notifierId, eventId, event, now, now) as { id: number };
    return r.id;
  }

  finishDelivery(
    id: number,
    result: { status: 'ok' | 'failed'; attempts: number; responseCode?: number; error?: string },
  ): void {
    this.#store.db
      .query(
        `UPDATE deliveries SET status = ?, attempts = ?, response_code = ?, error = ?, updated_at = ?
         WHERE id = ?`,
      )
      .run(
        result.status,
        result.attempts,
        result.responseCode ?? null,
        // First line only: a wall of HTML from an error page belongs in nobody's database.
        result.error ? result.error.split('\n')[0]!.slice(0, 300) : null,
        Date.now(),
        id,
      );
  }

  /** Mirror of the last outcome onto the notifier, so a list view answers "is this thing working". */
  noteResult(notifierId: number, status: 'ok' | 'failed'): void {
    this.#store.db
      .query('UPDATE notifiers SET last_status = ?, last_at = ? WHERE id = ?')
      .run(status, Date.now(), notifierId);
  }

  deliveries(notifierId: number, limit = 50): DeliveryRow[] {
    return (
      this.#store.db
        .query(
          `SELECT id, notifier_id, event_id, event, status, attempts, response_code, error,
                  created_at, updated_at
           FROM deliveries WHERE notifier_id = ? ORDER BY created_at DESC LIMIT ?`,
        )
        .all(notifierId, Math.min(Math.max(limit, 1), 500)) as Array<{
        id: number;
        notifier_id: number;
        event_id: string;
        event: string;
        status: string;
        attempts: number;
        response_code: number | null;
        error: string | null;
        created_at: number;
        updated_at: number;
      }>
    ).map((r) => ({
      id: r.id,
      notifierId: r.notifier_id,
      eventId: r.event_id,
      event: r.event,
      status: r.status,
      attempts: r.attempts,
      responseCode: r.response_code,
      error: r.error,
      createdAt: r.created_at,
      updatedAt: r.updated_at,
    }));
  }

  /**
   * Cap the log per notifier. Called after each delivery — cheap, and keeps the bound continuous.
   *
   * `status != 'pending'` is not decoration. A delivery retrying against a slow endpoint lives for up
   * to 21 seconds with its row already inserted; a burst of newer, faster deliveries in that window
   * would push it out of the keep-set and DELETE it, and the later `finishDelivery` would then update
   * zero rows — losing precisely the failure an operator went looking for. Rows still in flight are
   * never eligible; they become eligible the moment they finish.
   */
  prune(notifierId: number): void {
    this.#store.db
      .query(
        `DELETE FROM deliveries WHERE notifier_id = ? AND status != 'pending' AND id NOT IN (
           SELECT id FROM deliveries WHERE notifier_id = ? ORDER BY created_at DESC LIMIT ?
         )`,
      )
      .run(notifierId, notifierId, MAX_DELIVERIES_PER_NOTIFIER);
  }
}
