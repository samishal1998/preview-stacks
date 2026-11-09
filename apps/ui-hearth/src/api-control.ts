/** The control stack: containers, hostnames, certificates, Loki, the worker scaler. Same paths as apps/ui. */
import { onMounted, onUnmounted } from 'vue';
import type { JobStub, LokiSettings, LokiStorageInput, RuntimeContainer } from '@samyx/preview-stacks-client';
import { api, call, id, type Result } from './api';

export type ControlContainer = RuntimeContainer & { oomKilled: boolean; memLimitBytes: number | null };
export type ControlRuntime = { containers: ControlContainer[]; reachable: boolean };
export type TlsWildcard = { domains: string[]; notBefore: number; notAfter: number; issuer: string; selfSigned: boolean };
export type TlsStatus = { mode: string; traefik: string; wildcard: TlsWildcard | null; note: string };
export type TlsRedeploy = {
  started: Array<{ id: string; job: string }>;
  skipped: Array<{ id: string; reason: string }>;
  note: string;
};
export type DomainsStatus = { primary: string; domains: string[]; mode: string; note: string };
/** `GET /api/forward`: where this host forwards its deploys; `null` when it does not. */
export type ForwardStatus = { to: string; via: string | null; establishedAt: number; lastOkAt: number };
export type LokiSave = { job: JobStub } | { changed: false };

/** The worker scaler's settings as saved. `dismissed` names possible orphans an admin dismissed. */
export type ScalerSettings = {
  preset: string;
  vars: Record<string, string>;
  min: number;
  max: number;
  cooldownMin: number;
  graceMin: number;
  dismissed: string[];
};
export type ScalerWorker = { name: string; vars: Record<string, string>; state: string; createdAt: number; removing: boolean };
/** The scaler's last report; times in ms, `nextRetryAt` 0 when nothing waits. */
export type ScalerReport = {
  at: number;
  hostId: string;
  workers: ScalerWorker[];
  locked: { preset: string; filesHash: string; shared: Record<string, string> } | null;
  lastAction: string;
  lastError: string;
  nextRetryAt: number;
  doesNotFit: string[];
  orphans: string[];
  pendingDestroy: string;
};
/** `GET /api/scaler`. Credentials are key names only; `enabled` is null when docker did not answer. */
export type ScalerView = { settings: ScalerSettings; credentials: Record<string, true>; report: ScalerReport | null; enabled: boolean | null };
/** One Tofu preset; `files` only from the single-preset routes. */
export type ScalerPreset = {
  name: string;
  builtin: boolean;
  files?: Record<string, string>;
  preset: { perWorker: string[]; shared: string[]; distro: string; hash: string; lockFile: boolean };
  locked: boolean;
};
/** `PUT /api/scaler`. A credential sent empty keeps its value; an absent `credentials` changes none. */
export type ScalerInput = ScalerSettings & { credentials?: Record<string, string> };

export const controlApi = {
  runtime: () => call<ControlRuntime>('GET', '/api/control/runtime'),
  restart: (service: string) => call<{ container: string }>('POST', '/api/control/restart', { service }),
  domains: () => call<DomainsStatus>('GET', '/api/domains'),
  saveDomains: (domains: string[]) => call<DomainsStatus>('PUT', '/api/domains', { domains }),
  tls: () => call<TlsStatus>('GET', '/api/tls'),
  forward: () => call<ForwardStatus | null>('GET', '/api/forward'),
  storeWildcard: (cert: string, key: string) => call<{ wildcard: TlsWildcard; note: string }>('PUT', '/api/tls/wildcard', { cert, key }),
  removeWildcard: () => call<{ note: string }>('DELETE', '/api/tls/wildcard'),
  redeploy: () => call<TlsRedeploy>('POST', '/api/tls/redeploy', {}),
  logging: () => call<LokiSettings>('GET', '/api/logging'),
  saveLogging: (body: unknown) => call<LokiSave>('PUT', '/api/logging', body),
  saveStorage: (body: LokiStorageInput) => call<LokiSave>('PUT', '/api/logging/storage', body),
  job: (jobId: string) => api.job(jobId),
  scaler: () => call<ScalerView>('GET', '/api/scaler'),
  saveScaler: (body: ScalerInput) => call<ScalerView>('PUT', '/api/scaler', body),
  presets: () => call<ScalerPreset[]>('GET', '/api/scaler/presets'),
  preset: (name: string) => call<ScalerPreset>('GET', `/api/scaler/presets/${id(name)}`),
  savePreset: (name: string, files: Record<string, string>) => call<ScalerPreset>('PUT', `/api/scaler/presets/${id(name)}`, { files }),
  deletePreset: (name: string) => call<{ deleted: true }>('DELETE', `/api/scaler/presets/${id(name)}`),
};
export { id };

/** The error line for a failed call: the server's words, else the status. */
export function why<T>(r: Result<T>, what: string): string {
  if (r.status === 0) return r.body.error ?? 'The server did not answer.';
  if (r.status === 401) return `${what}: sign in again.`;
  if (r.status === 403) return `${what}: not allowed for your role.`;
  return `${what}: ${r.body.error ?? `HTTP ${r.status}`}`;
}

/** Runs `fn` now and every `ms` while the tab is visible. */
export function poll(fn: () => void | Promise<void>, ms: number): void {
  let t: ReturnType<typeof setInterval> | undefined;
  const stop = () => {
    if (t) clearInterval(t);
    t = undefined;
  };
  const start = () => {
    stop();
    t = setInterval(() => void fn(), ms);
  };
  const onVis = () => {
    if (document.visibilityState === 'visible') {
      void fn();
      start();
    } else stop();
  };
  onMounted(() => {
    void fn();
    if (document.visibilityState === 'visible') start();
    document.addEventListener('visibilitychange', onVis);
  });
  onUnmounted(() => {
    stop();
    document.removeEventListener('visibilitychange', onVis);
  });
}
