/**
 * The only place this app talks to the pstack API: same-origin, cookie session, the shapes typed by
 * `@samyx/preview-stacks-client`. Relative paths on purpose — served from the control hostname it
 * reaches that hostname's /api, so there is nothing to configure when the domain changes.
 */
import type { DeploymentRow, Job, Me } from '@samyx/preview-stacks-client';

export type Result<T> = { ok: boolean; status: number; body: T & { error?: string } };

async function call<T>(method: 'GET' | 'POST', path: string, body?: unknown): Promise<Result<T>> {
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

export const api = {
  me: () => call<Me>('GET', '/api/auth/me'),
  login: (username: string, password: string) => call<{ user: unknown }>('POST', '/api/auth/login', { username, password }),
  logout: () => call<unknown>('POST', '/api/auth/logout', {}),
  deployments: () => call<{ deployments: DeploymentRow[] }>('GET', '/api/deployments'),
  jobs: () => call<{ jobs: Job[] }>('GET', '/api/jobs'),
};
