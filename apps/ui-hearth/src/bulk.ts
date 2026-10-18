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

export const isTearDown = (a: BulkAction): boolean => a === 'down' || a === 'down-forget';

/** Why this deployment is left out of this action, or '' to send it. */
export function skipReason(d: DeploymentRow, a: BulkAction): string {
  if (d.unresolved || !d.stack) return 'needs variables';
  if (d.kind === 'shared' && isTearDown(a)) return 'shared: tear down from its own page';
  if (d.kind === 'shared' && a === 'sleep') return 'shared stacks do not sleep';
  if (a === 'sleep' && !d.orchestrator) return 'no compose section';
  if (a === 'sleep' && d.asleep) return 'already asleep';
  if (a === 'wake' && !d.asleep) return 'not asleep';
  return '';
}

/** The selected rows that are still shown. A row a filter hid is never acted on. */
export function chosen(shown: DeploymentRow[], selected: string[]): DeploymentRow[] {
  const set = new Set(selected);
  return shown.filter((d) => set.has(d.id));
}

/** How many of these the action would actually send. */
export const sendable = (rows: DeploymentRow[], a: BulkAction): number => rows.filter((d) => !skipReason(d, a)).length;
