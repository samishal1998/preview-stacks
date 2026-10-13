/**
 * What a bulk action sends and what it skips. Shared stacks are never torn down in bulk: their own
 * page asks for the stack name typed out, and a checkbox in a list must not be a way around that.
 */
import type { DeploymentRow } from '@samyx/preview-stacks-client';

export type BulkAction = 'up' | 'verify' | 'sleep' | 'wake' | 'down' | 'down-forget';

export const BULK_LABEL: Record<BulkAction, string> = {
  up: 'Deploy',
  verify: 'Verify',
  sleep: 'Sleep',
  wake: 'Wake',
  down: 'Tear down',
  'down-forget': 'Tear down and forget',
};

/** Why this deployment is left out of this action, or '' to send it. */
export function skipReason(d: DeploymentRow, a: BulkAction): string {
  if (d.unresolved || !d.stack) return 'needs variables';
  const tearsDown = a === 'down' || a === 'down-forget';
  if (d.kind === 'shared' && tearsDown) return 'shared — tear down from its own page';
  if (d.kind === 'shared' && a === 'sleep') return 'shared stacks do not sleep';
  if (a === 'sleep' && !d.orchestrator) return 'no compose section';
  if (a === 'sleep' && d.asleep) return 'already asleep';
  if (a === 'wake' && !d.asleep) return 'not asleep';
  return '';
}
