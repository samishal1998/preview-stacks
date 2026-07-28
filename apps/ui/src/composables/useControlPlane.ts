/**
 * Shared control-plane state: health, the deployment list, the job list.
 *
 * A module-level singleton because three views and the nav rail all show counts from the same two
 * endpoints, and giving each its own copy would mean N polls per interval against a host that is
 * also running everyone's previews. One fetch, everyone reads it.
 *
 * Errors are held as text, not thrown: a failed refresh must degrade one panel, never blank the
 * page. `loaded` distinguishes "not fetched yet" (show a skeleton) from "fetched, and empty"
 * (show the empty state) — on this page those two must never look the same.
 */

import { computed, reactive } from 'vue';
import { api, problem } from '../api/client';
import type { DeploymentRow, DeploymentsResponse, Health, Job, JobsResponse } from '../api/types';

export const state = reactive({
  health: null as Health | null,
  healthError: '',

  deployments: [] as DeploymentRow[],
  deploymentsError: '',
  deploymentsLoaded: false,

  jobs: [] as Job[],
  jobsError: '',
  jobsLoaded: false,
});

export async function loadHealth(): Promise<void> {
  const r = await api.get<Health>('/api/health');
  if (r.ok) {
    state.health = r.body;
    state.healthError = '';
  } else {
    state.health = null;
    state.healthError = problem(r, 'GET /api/health');
  }
}

export async function loadDeployments(): Promise<void> {
  const r = await api.get<DeploymentsResponse>('/api/deployments');
  state.deploymentsLoaded = true;
  if (!r.ok) {
    state.deploymentsError = problem(r, 'GET /api/deployments');
    return;
  }
  state.deploymentsError = '';
  state.deployments = (r.body.deployments ?? []).map((d) => ({
    ...d,
    kind: d.kind === 'shared' ? 'shared' : 'isolated',
    // `?? null`, never `|| null`: `false` is a real answer and must not become "could not tell".
    busy: d.busy ?? null,
    running: d.running ?? null,
    stack: d.stack ?? null,
  }));
}

export async function loadJobs(): Promise<void> {
  const r = await api.get<JobsResponse>('/api/jobs');
  state.jobsLoaded = true;
  if (!r.ok) {
    state.jobsError = problem(r, 'GET /api/jobs');
    return;
  }
  state.jobsError = '';
  state.jobs = r.body.jobs ?? [];
}

/** Counts for the dashboard. An unknown gets its OWN bucket — never folded into the false side. */
export const summary = computed(() => {
  const c = {
    total: 0,
    isolated: 0,
    shared: 0,
    running: 0,
    busy: 0,
    unknown: 0,
    unresolved: 0,
  };
  for (const d of state.deployments) {
    c.total++;
    if (d.kind === 'shared') c.shared++;
    else c.isolated++;
    if (d.busy === true) c.busy++;
    if (d.running === true) c.running++;
    if (d.busy === null || d.running === null) c.unknown++;
    if (d.unresolved) c.unresolved++;
  }
  return c;
});

/** Leaked jobs are the one thing worth surfacing above everything else on the dashboard. */
export const leakedJobs = computed(() => state.jobs.filter((j) => j.state === 'leaked'));

/** Look a deployment's list row up by id — the only place `vars`/`specName` are exposed today. */
export function rowFor(id: string): DeploymentRow | undefined {
  return state.deployments.find((d) => d.id === id);
}
