/**
 * The only place this app talks to the pstack API: same-origin, cookie session, shapes typed by
 * `@samyx/preview-stacks-client`. Relative paths on purpose — served from the control hostname it
 * reaches that hostname's /api, so nothing needs configuring when the domain changes.
 *
 * A non-2xx is not an exception: `call` always resolves, and callers branch on `status`.
 */
import type { DeploymentRow, Job, JobStub, Logs, Me, Runtime } from '@samyx/preview-stacks-client';

export type Result<T> = { ok: boolean; status: number; body: T & { error?: string } };
export type Lifecycle = 'up' | 'verify' | 'down' | 'sleep' | 'wake';

async function call<T>(method: 'GET' | 'POST' | 'DELETE', path: string, body?: unknown): Promise<Result<T>> {
  try {
    const r = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: body === undefined ? undefined : { 'content-type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const parsed = (await r.json().catch(() => ({}))) as T & { error?: string };
    return { ok: r.ok, status: r.status, body: parsed };
  } catch {
    return { ok: false, status: 0, body: { error: 'the server did not answer' } as T & { error?: string } };
  }
}

const id = encodeURIComponent;

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
