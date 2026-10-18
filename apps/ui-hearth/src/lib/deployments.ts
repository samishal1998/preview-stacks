/** Deployment list rules: the run state shown, the counts, and the filters. Unknown is never zero. */
import type { DeploymentRow } from '@samyx/preview-stacks-client';

export type RunState = 'busy' | 'asleep' | 'running' | 'unknown' | 'idle';

/** `busy` and `running` are null when the server could not tell; that is not idle. */
export function runState(d: Pick<DeploymentRow, 'busy' | 'running' | 'asleep'>): RunState {
  if (d.busy === true) return 'busy';
  if (d.asleep) return 'asleep';
  if (d.running === true) return 'running';
  if (d.busy == null || d.running == null) return 'unknown';
  return 'idle';
}

export function summarize(list: DeploymentRow[]) {
  const c = { total: 0, isolated: 0, shared: 0, running: 0, busy: 0, asleep: 0, unknown: 0, unresolved: 0 };
  for (const d of list) {
    c.total++;
    if (d.kind === 'shared') c.shared++;
    else c.isolated++;
    if (d.busy === true) c.busy++;
    if (d.asleep) c.asleep++;
    if (d.running === true) c.running++;
    if (d.busy == null || d.running == null) c.unknown++;
    if (d.unresolved) c.unresolved++;
  }
  return c;
}

export function filterDeployments(list: DeploymentRow[], f: { q: string; kinds: string[]; states: string[] }): DeploymentRow[] {
  const needle = f.q.trim().toLowerCase();
  return list.filter((d) => {
    if (f.kinds.length && !f.kinds.includes(d.kind)) return false;
    if (f.states.length && !f.states.includes(runState(d))) return false;
    if (!needle) return true;
    return d.id.toLowerCase().includes(needle) || (d.stack ?? '').toLowerCase().includes(needle) || (d.specName ?? '').toLowerCase().includes(needle);
  });
}
