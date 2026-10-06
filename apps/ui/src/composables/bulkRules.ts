/**
 * Which deployments a bulk action sends, and what it calls itself. Pure — no API client, no DOM —
 * so `test/bulkRules.test.ts` can prove it; `useBulkActions.ts` does the sending.
 */
import type { DeploymentRow } from '../api/types';

export type BulkAction = 'up' | 'verify' | 'sleep' | 'wake' | 'down' | 'down-forget';

export const BULK_LABELS: Record<BulkAction, string> = {
  up: 'Deploy',
  verify: 'Verify',
  sleep: 'Sleep',
  wake: 'Wake',
  down: 'Tear down',
  'down-forget': 'Tear down and forget',
};

/** Why this deployment is left out of this action, or '' to send it. */
export function skipReason(d: DeploymentRow, action: BulkAction): string {
  if (d.unresolved || !d.stack) return 'needs variables';
  const tearsDown = action === 'down' || action === 'down-forget';
  if (d.kind === 'shared' && tearsDown) return 'shared — tear down from its own page';
  if (d.kind === 'shared' && action === 'sleep') return 'shared stacks do not sleep';
  if (action === 'sleep' && !d.orchestrator) return 'no compose section';
  if (action === 'sleep' && d.asleep) return 'already asleep';
  if (action === 'wake' && !d.asleep) return 'not asleep';
  return '';
}
