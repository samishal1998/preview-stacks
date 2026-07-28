/**
 * The one place this app talks to the network.
 *
 * Everything about the pstack API that a caller could get wrong lives here so it is got wrong in
 * exactly one place:
 *
 *   AUTH goes on POST/PUT/DELETE only. Not an oversight — `EventSource` cannot send headers, so
 *   the live job log would be impossible if GETs were authenticated. The server's second guard
 *   covers the gap: with no token configured it binds 127.0.0.1 and refuses 0.0.0.0.
 *
 *   NOTHING IS EVER RETRIED. Every mutating route on this API destroys or creates infrastructure,
 *   and a 409 in particular means "refused, and nothing was started" — retrying it is how you turn
 *   a clear refusal into a race.
 *
 *   A NON-2xx IS NOT AN EXCEPTION. `request` always resolves. Callers branch on `status`, because
 *   the interesting statuses (400 carrying a SpecError, 409 carrying a refusal) are content, not
 *   failure — and a thrown error in a view renders a blank screen.
 */

import type { ConflictBody, VarPair } from './types';
import { settings } from '../composables/useSettings';

export type ApiResult<T> = {
  status: number;
  ok: boolean;
  /** Parsed JSON, or `{}` when the body was empty or not JSON. Never null, so `.error` is safe. */
  body: T & { error?: string };
  /** The raw text, kept for the not-JSON case. */
  raw: string;
  /** true ⇒ something other than this API answered: a proxy, a login page, an error page. */
  notJson: boolean;
};

/** `?PR=7&REGION=eu`, plus any extras. Rows with an empty name are dropped, not sent as `=v`. */
export function query(pairs: VarPair[], extra: Record<string, string | number> = {}): string {
  const p = new URLSearchParams();
  for (const e of pairs) if (e.k) p.set(e.k, e.v ?? '');
  for (const [k, v] of Object.entries(extra)) p.set(k, String(v));
  const s = p.toString();
  return s ? `?${s}` : '';
}

/**
 * Same-origin by default — which is the whole reason the container ships an nginx `/api/` proxy.
 * An absolute base is offered in Settings for a dev session against a tunnelled host, and it can
 * only work if something in front of that host adds CORS headers: the API adds none, and
 * `EventSource` is subject to the same rule with no way to opt out.
 */
function url(path: string): string {
  const base = settings.apiBase.replace(/\/$/, '');
  return base ? base + path : path;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<ApiResult<T>> {
  const headers: Record<string, string> = {};
  if (method !== 'GET') {
    headers['content-type'] = 'application/json';
    // PUT and DELETE destroy just as thoroughly as POST, so all three carry the token.
    if (settings.token) headers.authorization = `Bearer ${settings.token}`;
  }
  try {
    const res = await fetch(url(path), {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const raw = await res.text();
    let parsed: unknown = null;
    try {
      parsed = raw ? JSON.parse(raw) : null;
    } catch {
      /* handled by notJson below */
    }
    if (parsed === null && raw) {
      return { status: res.status, ok: res.ok, body: {} as T, raw, notJson: true };
    }
    return { status: res.status, ok: res.ok, body: (parsed ?? {}) as T, raw, notJson: false };
  } catch (e) {
    // The API being unreachable entirely — wrong base URL, container down, DNS. Status 0 so no
    // caller can mistake it for an answer the server gave.
    return {
      status: 0,
      ok: false,
      body: { error: `unreachable: ${(e as Error).message}` } as T & { error: string },
      raw: '',
      notJson: false,
    };
  }
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body ?? {}),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
  /** For `EventSource`, which builds its own connection and cannot go through `request`. */
  url,
};

/**
 * A non-2xx, turned into a sentence that names the call.
 *
 * The point is to never render a bare status. A 404 is genuinely ambiguous — "no such deployment"
 * or "this build of the API has no such route" — and those are very different problems, so say
 * both rather than picking one.
 */
export function problem(res: ApiResult<unknown>, what: string): string {
  if (res.status === 0) return res.body.error ?? 'unreachable';
  if (res.status === 401) {
    return `${what}: 401 unauthorized — set the bearer token in Settings.`;
  }
  if (res.notJson) {
    return (
      `${what}: HTTP ${res.status}, and the response was not JSON. Something other than the ` +
      `pstack API answered — a proxy, or an error page.\n\n${res.raw.slice(0, 400)}`
    );
  }
  const e = res.body.error;
  if (res.status === 404 && (!e || e === 'not found')) {
    return `${what}: 404 — no such resource, or this build of the API has no such route yet.`;
  }
  return e ? `${what}: ${e}` : `${what}: HTTP ${res.status}`;
}

export type Conflict = {
  /**
   * 'shared'     the `kind: shared` down refusal — the fix is the typed confirmation, not waiting.
   * 'containers' DELETE refused because containers still exist — the fix is `down` first.
   * 'referenced' DELETE of a spec still referenced by deployments.
   * 'other'      a job in flight, OR "docker did not answer". These two are shape-identical, so
   *              they are deliberately NOT told apart: print the message, offer nothing but a link
   *              to a running job if one demonstrably exists.
   */
  kind: 'shared' | 'containers' | 'referenced' | 'other';
  text: string;
  stack: string;
  deployments: string[];
};

/** Classify a 409 by the SHAPE of its payload. Never by matching the message text. */
export function classifyConflict(body: ConflictBody): Conflict {
  const text = body.error ?? 'HTTP 409 (no message)';
  const stack = body.stack ?? '';
  if (body.kind) return { kind: 'shared', text, stack, deployments: [] };
  if (body.containers !== undefined) return { kind: 'containers', text, stack, deployments: [] };
  if (body.deployments) return { kind: 'referenced', text, stack, deployments: body.deployments };
  return { kind: 'other', text, stack, deployments: [] };
}
