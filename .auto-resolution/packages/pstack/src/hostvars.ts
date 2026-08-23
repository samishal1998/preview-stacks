/**
 * Host-level variables and secrets — the GitHub-Actions model, scoped to one host.
 *
 * A VARIABLE is configuration everyone may read: a region, a base image tag, a feature flag. A
 * SECRET is a credential: its value goes IN through the API and never comes back OUT — list()
 * returns names and dates only, and the value is scrubbed from job and compose logs by content.
 *
 * Specs reference them EXPLICITLY: `${vars.REGION}`, `${secrets.DB_PASSWORD}`. The namespace is the
 * point — a plain `${REGION}` keeps meaning "whatever the request or the deployment supplied", and
 * a spec that reads host state says so on its face. No precedence question can arise between a
 * request variable and a host variable because they are spelled differently.
 *
 * ONE KIND CHANGE IS REFUSED: secret → variable. Flipping the flag would turn a write-only value
 * into a readable one with no re-entry of the value — an information-flow downgrade wearing an
 * UPDATE's clothes. Going the other way (variable → secret) only tightens, so it is allowed.
 */

import type { Store } from './store.ts';

export class HostVarsError extends Error {}

/** Same shape spec interpolation accepts — anything else could never be referenced anyway. */
const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

export type HostVarRow = {
  name: string;
  /** Present for variables; ALWAYS null for secrets — the type mirrors the API's guarantee. */
  value: string | null;
  secret: boolean;
  updatedAt: number;
};

export class HostVars {
  #store: Store;

  constructor(store: Store) {
    this.#store = store;
  }

  /** What a caller may see: variable values, secret NAMES. */
  list(): HostVarRow[] {
    return (
      this.#store.db
        .query('SELECT name, value, secret, updated_at FROM host_vars ORDER BY name')
        .all() as Array<{ name: string; value: string; secret: number; updated_at: number }>
    ).map((r) => ({
      name: r.name,
      value: r.secret === 1 ? null : r.value,
      secret: r.secret === 1,
      updatedAt: r.updated_at,
    }));
  }

  /**
   * The full maps, for spec RESOLUTION only. Named to be conspicuous in a diff, like `secretOf` in
   * webhooks.ts — a second caller outside the resolve path is the moment to ask why.
   */
  resolveMaps(): { vars: Record<string, string>; secrets: Record<string, string> } {
    const vars: Record<string, string> = {};
    const secrets: Record<string, string> = {};
    const rows = this.#store.db
      .query('SELECT name, value, secret FROM host_vars')
      .all() as Array<{ name: string; value: string; secret: number }>;
    for (const r of rows) (r.secret === 1 ? secrets : vars)[r.name] = r.value;
    return { vars, secrets };
  }

  /** Every secret VALUE, for log scrubbing. Values, not names — logs leak by content. */
  secretValues(): string[] {
    return (
      this.#store.db.query('SELECT value FROM host_vars WHERE secret = 1').all() as Array<{
        value: string;
      }>
    ).map((r) => r.value);
  }

  put(name: string, value: string, secret: boolean): { created: boolean } {
    if (!NAME.test(name)) {
      throw new HostVarsError(
        `"${name}" is not a usable name — letters, digits and _ only, not starting with a digit ` +
          `(it has to be referenceable as \${${secret ? 'secrets' : 'vars'}.NAME} in a spec)`,
      );
    }
    if (value === '') {
      throw new HostVarsError(
        'an empty value would make every spec referencing it fail to resolve — delete the entry instead',
      );
    }
    const existing = this.#store.db
      .query('SELECT secret FROM host_vars WHERE name = ?')
      .get(name) as { secret: number } | null;
    if (existing && existing.secret === 1 && !secret) {
      throw new HostVarsError(
        `"${name}" is a secret. Making it a readable variable would reveal a value that was ` +
          `stored write-only — delete it and create a variable with the value re-entered instead.`,
      );
    }
    const now = Date.now();
    this.#store.db
      .query(
        `INSERT INTO host_vars (name, value, secret, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
         ON CONFLICT(name) DO UPDATE SET value = excluded.value, secret = excluded.secret,
         updated_at = excluded.updated_at`,
      )
      .run(name, value, secret ? 1 : 0, now, now);
    return { created: existing === null };
  }

  remove(name: string): boolean {
    return this.#store.db.query('DELETE FROM host_vars WHERE name = ?').run(name).changes > 0;
  }
}
