/**
 * The only place this app talks to the pstack API: same-origin, cookie session, shapes typed by
 * `@samyx/preview-stacks-client`. Relative paths on purpose — served from the control hostname it
 * reaches that hostname's /api, so nothing needs configuring when the domain changes.
 *
 * A non-2xx is not an exception: `call` always resolves, and callers branch on `status`.
 */
import { clearToken, getToken } from './lib/token';
import type { DeploymentRow, Job, JobStub, Logs, Me, Runtime } from '@samyx/preview-stacks-client';

export type Result<T> = { ok: boolean; status: number; body: T & { error?: string } };
export type Lifecycle = 'up' | 'verify' | 'down' | 'sleep' | 'wake';

/** Called on a 401 from any call but the sign-in probes themselves: the session or token is gone. */
let unauthorized: (() => void) | undefined;
export function onUnauthorized(fn: () => void): void {
  unauthorized = fn;
}

/** `Authorization` for the stored bearer token, if any. */
export function authHeaders(): Record<string, string> {
  const t = getToken();
  return t ? { authorization: `Bearer ${t}` } : {};
}

/** A 401 means the stored token (if any) no longer works; drop it and tell the shell. */
export function noteStatus(path: string, status: number): void {
  if (status !== 401 || path.startsWith('/api/auth/')) return;
  clearToken();
  unauthorized?.();
}

export async function call<T>(method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, body?: unknown): Promise<Result<T>> {
  try {
    const r = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: { ...authHeaders(), ...(body === undefined ? {} : { 'content-type': 'application/json' }) },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    noteStatus(path, r.status);
    const parsed = (await r.json().catch(() => ({}))) as T & { error?: string };
    return { ok: r.ok, status: r.status, body: parsed };
  } catch {
    return { ok: false, status: 0, body: { error: 'the server did not answer' } as T & { error?: string } };
  }
}

export const id = encodeURIComponent;

export const api = {
  me: () => call<Me>('GET', '/api/auth/me'),
  login: (username: string, password: string) => call<unknown>('POST', '/api/auth/login', { username, password }),
  logout: () => call<unknown>('POST', '/api/auth/logout', {}),
  deployments: () => call<{ deployments: DeploymentRow[] }>('GET', '/api/deployments'),
  deployment: (d: string) => call<DeploymentRow>('GET', `/api/deployments/${id(d)}`),
  runtime: (d: string) => call<Runtime>('GET', `/api/deployments/${id(d)}/runtime`),
  logs: (d: string, o: { service?: string; tail?: number; timestamps?: boolean } = {}) => {
    const q = new URLSearchParams({ tail: String(o.tail ?? 300) });
    if (o.service) q.set('service', o.service);
    if (o.timestamps) q.set('timestamps', '1');
    return call<Logs>('GET', `/api/deployments/${id(d)}/logs?${q}`);
  },
  jobs: () => call<{ jobs: Job[] }>('GET', '/api/jobs'),
  job: (j: string) => call<{ job: Job }>('GET', `/api/jobs/${id(j)}`),
  act: (d: string, action: Lifecycle, body: { verify?: boolean; forget?: boolean; force?: boolean } = {}) =>
    call<{ job: JobStub }>('POST', `/api/deployments/${id(d)}/${action}`, action === 'down' ? { verify: true, ...body } : {}),
  cancel: (d: string) => call<unknown>('POST', `/api/deployments/${id(d)}/cancel`, {}),
  forget: (d: string) => call<unknown>('DELETE', `/api/deployments/${id(d)}`),
};
