/**
 * Calls for one deployment: detail, runtime, logs, container verbs, share links, cancel, forget.
 * Lifecycle (up/down/verify/sleep/wake) goes through here too so a 409 keeps its typed body.
 *
 * A share link's token rides in the query string on every call the public page makes, because an
 * EventSource cannot set a header. It is passed in, never stored.
 */
import type { DeploymentRow, JobStub, Me, Readiness, Runtime, ShareLink } from '@samyx/preview-stacks-client';
import { call, id, type Lifecycle } from './api';
import type { ConflictBody } from './lib/deployment';
import { varsQuery } from './lib/vars';
import { readVars } from './vars/store';

/** The detail route: the row plus what the spec resolved to. */
export type DeploymentDetail = DeploymentRow & {
  compose?: {
    file: string;
    profiles: string[];
    overlays: string[];
    subdomains?: Array<{ profile: string; host: string; depth: 'one' | 'any'; varName: string; rule: string }>;
  } | null;
  requires?: unknown[];
  axes?: Array<{ name: string; hooks: string[]; verifiable?: boolean }>;
};

export type CancelResult = { stack: string; cancelled: JobStub[]; by: string; warning: string };
export type LogOpts = { service?: string; tail?: number; since?: string; timestamps?: boolean };

const withToken = (path: string, token?: string): string =>
  token ? `${path}${path.includes('?') ? '&' : '?'}token=${encodeURIComponent(token)}` : path;

function logQuery(o: LogOpts): string {
  const q = new URLSearchParams({ tail: String(o.tail ?? 200) });
  if (o.service) q.set('service', o.service);
  if (o.since) q.set('since', o.since);
  if (o.timestamps) q.set('timestamps', '1');
  return q.toString();
}

const base = (d: string): string => `/api/deployments/${id(d)}`;

/** The request variables saved for this deployment in this browser, as `?A=1`. Share-link calls pass a token instead and never use these. */
const vq = (d: string): string => varsQuery(readVars(d));

export const deploymentApi = {
  detail: (d: string, token?: string) => call<DeploymentDetail>('GET', withToken(base(d) + (token ? '' : vq(d)), token)),
  runtime: (d: string, token?: string) => call<Runtime>('GET', withToken(`${base(d)}/runtime${token ? '' : vq(d)}`, token)),
  readiness: (d: string) => call<Readiness>('GET', `${base(d)}/readiness`),
  logs: (d: string, o: LogOpts, token?: string) =>
    call<{ stack: string; tail: number; service: string | null; since?: string | null; lines?: number; ok: boolean; text: string }>(
      'GET',
      withToken(`${base(d)}/logs?${logQuery(o)}`, token),
    ),
  /** For `new EventSource(...)`: one followed process on the server, each line sent once. */
  streamUrl: (d: string, o: LogOpts, token?: string) => withToken(`${base(d)}/logs/stream?${logQuery(o)}`, token),
  me: (token: string) => call<Me>('GET', withToken('/api/auth/me', token)),

  act: (d: string, action: Lifecycle, body: { verify?: boolean; force?: boolean; forget?: boolean } = {}) =>
    call<{ job?: JobStub } & ConflictBody>(
      'POST',
      `${base(d)}/${action}${vq(d)}`,
      action === 'down' ? { verify: body.verify ?? true, force: body.force ?? false, ...(body.forget ? { forget: true } : {}) } : {},
    ),
  cancel: (d: string) => call<CancelResult>('POST', `${base(d)}/cancel${vq(d)}`, {}),
  forget: (d: string) => call<ConflictBody>('DELETE', base(d) + vq(d)),
  container: (d: string, name: string, action: 'start' | 'stop' | 'restart') =>
    call<{ note?: string }>('POST', `${base(d)}/containers/${id(name)}/${action}${vq(d)}`, {}),
  share: (d: string, views: string[], ttl: string) => call<ShareLink>('POST', `${base(d)}/share`, { views, ttl }),
};

/** The shell WebSocket. The session cookie authenticates the handshake; there is never a token in this URL. */
export function terminalUrl(d: string, container: string, shell: string): string {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${location.host}${base(d)}/terminal?${new URLSearchParams({ container, shell })}`;
}

export const sharePath = (d: string): string => `${base(d)}/share`;
