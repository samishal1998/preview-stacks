/**
 * One lifecycle action, applied to several deployments from the list.
 *
 * No bulk API: each deployment gets the same POST its own page would send, one after another. Each
 * is its own job anyway, and the server already queues per stack, so a bulk endpoint would only
 * add a second way to say the same thing. The browser does not wait for the jobs — every result
 * links to its job, and the jobs keep running if the page is closed.
 *
 * What is SKIPPED is decided here, before anything is sent, and said per row:
 *  - a deployment that needs variables cannot be resolved from the list;
 *  - a shared deployment is never torn down in bulk — its page asks for the stack name typed out,
 *    and a checkbox in a list must not be a way around that;
 *  - sleep and wake only where they mean something (a shared stack does not sleep; only an asleep
 *    one wakes).
 */
import { reactive } from 'vue';
import { api, problem } from '../api/client';
import type { ActionResponse, DeploymentRow, JobStub } from '../api/types';
import { BULK_LABELS, skipReason, type BulkAction } from './bulkRules';
import { loadDeployments, loadJobs } from './useControlPlane';

export type BulkResult = { id: string; job?: JobStub; error?: string; skipped?: string };

export const bulk = reactive({
  running: false,
  action: '' as BulkAction | '',
  results: [] as BulkResult[],
});

export async function runBulk(rows: DeploymentRow[], action: BulkAction): Promise<void> {
  bulk.running = true;
  bulk.action = action;
  bulk.results = [];
  for (const d of rows) {
    const skipped = skipReason(d, action);
    if (skipped) {
      bulk.results.push({ id: d.id, skipped });
      continue;
    }
    const verb = action === 'down-forget' ? 'down' : action;
    // Teardown always checks for leftovers here: in bulk, nobody is watching each one finish.
    const body = verb === 'down' ? { verify: true, ...(action === 'down-forget' ? { forget: true } : {}) } : undefined;
    const r = await api.post<ActionResponse>(`/api/deployments/${encodeURIComponent(d.id)}/${verb}`, body);
    if (r.status === 202 && r.body?.job) bulk.results.push({ id: d.id, job: r.body.job });
    else bulk.results.push({ id: d.id, error: problem(r, BULK_LABELS[action].toLowerCase()) });
  }
  bulk.running = false;
  void loadDeployments();
  void loadJobs();
}
