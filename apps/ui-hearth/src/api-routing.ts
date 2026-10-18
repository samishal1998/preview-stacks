import { call, id } from './api';

export type RoutingFile = { name: string; size: number; updatedAt: number };
export type RoutingList = { dir: string; writable: boolean; files: RoutingFile[] };
export type RoutingRead = { name: string; content?: string; sourceWithheld?: true };
export type RoutingWrite = { name?: string; deleted?: string; previous: string | null };
import type { RuntimeRoute } from '@samyx/preview-stacks-client';

export type { RuntimeRoute };

export const routingApi = {
  list: () => call<RoutingList>('GET', '/api/routing'),
  live: () => call<{ reachable: boolean; routes: RuntimeRoute[] }>('GET', '/api/routing/live'),
  read: (name: string) => call<RoutingRead>('GET', `/api/routing/${id(name)}`),
  write: (name: string, content: string) => call<RoutingWrite>('PUT', `/api/routing/${id(name)}`, { content }),
  remove: (name: string) => call<RoutingWrite>('DELETE', `/api/routing/${id(name)}`),
};
