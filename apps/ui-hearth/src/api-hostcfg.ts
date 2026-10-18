/** Host variables/secrets and the sealed-config apply. Same paths and bodies as apps/ui. */
import { call, id } from './api';
import type { Applied, Entry, Preview } from './lib/hostcfg';

export const hostcfg = {
  list: () => call<{ entries: Entry[] }>('GET', '/api/host-vars'),
  put: (name: string, value: string, secret: boolean) => call<unknown>('PUT', `/api/host-vars/${id(name)}`, { value, secret }),
  del: (name: string) => call<unknown>('DELETE', `/api/host-vars/${id(name)}`),
  sealed: (sealed: string, passphrase: string, preview: boolean) =>
    call<Preview | Applied>('POST', '/api/config/sealed', { sealed, passphrase, preview }),
};

/** Why a call failed, in one line. */
export const failure = (r: { status: number; body: { error?: string } }): string =>
  r.status === 401 ? 'Sign in again.' : r.status === 403 ? 'Admin only.' : (r.body.error ?? `HTTP ${r.status}`);
