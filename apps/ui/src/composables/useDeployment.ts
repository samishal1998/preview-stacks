/**
 * The deployment currently open in the detail view.
 *
 * A module singleton rather than provide/inject, because the six tabs are CHILD ROUTES: they are
 * siblings of each other and are mounted and unmounted independently, so passing the loaded
 * deployment down as a prop would mean re-fetching it on every tab change. Switching tabs must not
 * re-fetch or blank the panel — only opening a DIFFERENT deployment does.
 *
 * `vars` here are REQUEST-TIME variables, sent as `?query` on every route that resolves this
 * deployment. See `useVars.ts` for why they matter and what they can get wrong.
 */

import { computed, reactive } from 'vue';
import { api, problem, query } from '../api/client';
import type { Deployment, VarPair } from '../api/types';
import { readVars, writeVars } from './useVars';
import { rowFor } from './useControlPlane';

export const dep = reactive({
  id: '',
  detail: null as Deployment | null,
  /** The resolve failure, verbatim. It is a 400 that NAMES the missing variable. */
  error: '',
  loading: false,
  vars: [] as VarPair[],
});

export const varsQuery = computed(() => query(dep.vars));

/** The list row, which is the only place the server exposes stored `vars` and `specName` today. */
export const row = computed(() => rowFor(dep.id));

export const isShared = computed(() => dep.detail?.kind === 'shared');

export const unverifiableAxes = computed(() =>
  (dep.detail?.axes ?? []).filter((a) => !a.verifiable).map((a) => a.name),
);

export async function loadDetail(): Promise<void> {
  const id = dep.id;
  dep.loading = true;
  const r = await api.get<Deployment>(`/api/deployments/${encodeURIComponent(id)}${varsQuery.value}`);
  // Navigated to another deployment while this was in flight: drop the answer rather than render
  // one deployment's spec under another's id.
  if (dep.id !== id) return;
  dep.loading = false;

  if (!r.ok) {
    dep.detail = null;
    dep.error = problem(r, `GET /api/deployments/${id}`);
    return;
  }
  dep.error = '';
  const b = r.body;
  dep.detail = {
    id: b.id ?? id,
    kind: b.kind === 'shared' ? 'shared' : 'isolated',
    createdAt: b.createdAt,
    updatedAt: b.updatedAt,
    stack: typeof b.stack === 'string' ? b.stack : '',
    busy: b.busy ?? null,
    orchestrator: b.orchestrator ?? null,
    sleep: b.sleep ?? null,
    asleep: b.asleep ?? null,
    compose: b.compose
      ? {
          file: b.compose.file ?? '?',
          profiles: b.compose.profiles ?? [],
          overlays: b.compose.overlays ?? [],
        }
      : null,
    requires: Array.isArray(b.requires) ? b.requires : [],
    axes: (Array.isArray(b.axes) ? b.axes : []).map((a) => ({
      name: a.name,
      hooks: Array.isArray(a.hooks) ? a.hooks : [],
      // Derive it if an older build omits the field. Defaulting to `false` would claim an axis is
      // unverifiable when it may define a perfectly good `assert_gone`.
      verifiable: a.verifiable ?? (Array.isArray(a.hooks) && a.hooks.includes('assert_gone')),
    })),
    env: Array.isArray(b.env) ? b.env : [],
  };
}

/** Point the singleton at a deployment. Resets everything a stale id could leak into the new one. */
export function openDeployment(id: string): void {
  if (dep.id === id && (dep.detail || dep.error)) return; // a tab change, not a navigation
  dep.id = id;
  dep.detail = null;
  dep.error = '';
  dep.vars = readVars(id);
  void loadDetail();
}

export function persistVars(): void {
  writeVars(dep.id, dep.vars);
}

export function applyVars(): void {
  persistVars();
  void loadDetail();
}
