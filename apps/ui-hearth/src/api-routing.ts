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

/** `/api/custom-routes`: a stable alias for a stack. `deployment` and `stack` are null when unassigned. */
export type CustomRoute = {
  name: string;
  deployment: string | null;
  stack: string | null;
  hosts: string[];
  /** Hosts a deployment or an earlier route serves instead. */
  blocked: Array<{ host: string; by: { kind: 'deployment' | 'route'; name: string } }>;
  createdAt: number;
  updatedAt: number;
};
/** A non-force PUT on a route another deployment holds. */
export type RouteHeld = { assignedTo?: { deployment: string; stack: string | null } };

export const customRoutesApi = {
  list: () => call<CustomRoute[]>('GET', '/api/custom-routes'),
  create: (name: string, deployment?: string) => call<CustomRoute>('POST', '/api/custom-routes', deployment ? { name, deployment } : { name }),
  remove: (name: string) => call<{ deleted: string }>('DELETE', `/api/custom-routes/${id(name)}`),
  assign: (name: string, deployment: string, force = false) =>
    call<CustomRoute & RouteHeld>('PUT', `/api/custom-routes/${id(name)}/assignment`, force ? { deployment, force } : { deployment }),
  unassign: (name: string) => call<CustomRoute>('DELETE', `/api/custom-routes/${id(name)}/assignment`),
};
