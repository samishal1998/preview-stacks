import type { SwarmInfo } from '@samyx/preview-stacks-client';
import { authHeaders, call, id } from './api';

export type SignalNode = { id: string; hostname: string; role: string; state: string; availability: string; tasks: number; emptySince: number | null };
export type Signals = {
  swarm: boolean;
  reachable: boolean;
  at: number;
  nodes: SignalNode[];
  stuck: Array<{ task: string; service: string; stack: string; reason: string }>;
};

export const swarmApi = {
  info: () => call<SwarmInfo>('GET', '/api/swarm'),
  signals: () => call<Signals>('GET', '/api/signals'),
  drain: (node: string) => call<unknown>('POST', `/api/swarm/nodes/${id(node)}/drain`, {}),
  undrain: (node: string) => call<unknown>('POST', `/api/swarm/nodes/${id(node)}/undrain`, {}),
  remove: (node: string) => call<unknown>('DELETE', `/api/swarm/nodes/${id(node)}`),
  /** The join material is text/plain, which `call` (JSON only) cannot carry — the one raw fetch here. */
  async join(path: string): Promise<{ ok: boolean; status: number; text: string }> {
    try {
      const r = await fetch(path, { credentials: 'same-origin', headers: authHeaders() });
      const text = await r.text();
      if (r.ok) return { ok: true, status: r.status, text };
      let msg = text;
      try {
        msg = (JSON.parse(text) as { error?: string }).error ?? text;
      } catch {
        /* plain text already */
      }
      return { ok: false, status: r.status, text: msg };
    } catch {
      return { ok: false, status: 0, text: 'the server did not answer' };
    }
  },
};
