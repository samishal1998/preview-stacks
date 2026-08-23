/**
 * Step classification — the single most important logic in this app.
 *
 * A job's outcome has four states that MUST NOT blur into one another. The whole product is the
 * distinction between the middle two:
 *
 *   ok             passed. A `down` step is ALWAYS ok and may still carry a "non-fatal:" note —
 *                  teardown is best-effort by design, so the step really did pass.
 *   unverifiable   the axis defines no `assert_gone`, so `verify` had nothing to check. NOT a pass.
 *                  A green here would be exactly the false confidence this tool exists to remove.
 *   leaked         `assert_gone` FAILED — the resource survived teardown. Nothing else will clean
 *                  it up. This is the signal the whole tool exists for, and the CLI's exit code 2.
 *   failed         any other non-zero step.
 *
 * Order matters: `unverifiable` is matched on the message FIRST, exactly as the CLI's own report
 * does. `skipped` alone is not enough — a dry run also skips.
 */

import type { StepResult } from '../api/types';

export type StepState = 'ok' | 'unverifiable' | 'leaked' | 'failed';

export function stepState(s: StepResult): StepState {
  if (s.message?.startsWith('unverifiable')) return 'unverifiable';
  if (!s.ok) return s.phase === 'assert_gone' ? 'leaked' : 'failed';
  return 'ok';
}

export function stepMark(s: StepResult): string {
  return { ok: '✓', unverifiable: '?', leaked: '!', failed: '✗' }[stepState(s)];
}

export function stepText(s: StepResult): string {
  if (s.message) return s.message;
  if (s.skipped) return 'skipped';
  return stepState(s) === 'ok' ? 'ok' : `exit ${s.code}`;
}

/** Only a failed `assert_gone` is a leak. Every other non-zero step is a plain failure. */
export function leakedAxes(steps: StepResult[]): string[] {
  return steps.filter((s) => s.phase === 'assert_gone' && !s.ok).map((s) => s.axis);
}

export function countUnverifiable(steps: StepResult[]): number {
  return steps.filter((s) => stepState(s) === 'unverifiable').length;
}
