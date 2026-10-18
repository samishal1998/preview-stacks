/** Single sign-on: the stored providers (admin) and the public sign-in summary. */
import type { HostSettings, SsoConfigResponse } from '@samyx/preview-stacks-client';
import { call, id } from './api';
import type { SsoBody } from './lib/sso';

export type SignInProvider = { key: string; label: string; preset: string };
export type Health = { hasUsers?: boolean; grafana?: string; sso?: { providers: SignInProvider[] } | null };

export const ssoApi = {
  config: () => call<SsoConfigResponse>('GET', '/api/sso/config'),
  save: (b: SsoBody) => call<{ ok: boolean; key: string }>('PUT', '/api/sso/config', b),
  remove: (key: string) => call<unknown>('DELETE', `/api/sso/config/${id(key)}`),
  /** Best effort: an older server has no such route. */
  hostDefaultRole: async (): Promise<string> => {
    const r = await call<HostSettings>('GET', '/api/settings');
    return r.ok ? String(r.body.settings?.find((s) => s.key === 'default_role')?.value ?? '') : '';
  },
  /** Public, read before authenticating: whether accounts exist and which providers are enabled. */
  health: () => call<Health>('GET', '/api/health'),
  /** A full navigation, not a fetch: it leaves this origin for the provider. */
  startUrl: (key: string, next: string) => `/api/auth/sso/start?provider=${encodeURIComponent(key)}&next=${encodeURIComponent(next)}`,
};
