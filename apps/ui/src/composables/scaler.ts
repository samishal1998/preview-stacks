import type { ScalerSettings } from '../api/types';

/**
 * What the scaler draft refills on: the saved fields only, so a dismissal or a poll does not wipe typing.
 * A tuple, not an array replacer: a replacer also filters nested keys, so vars would read as {}.
 */
export const scalerSavedKey = (s: ScalerSettings): string =>
  JSON.stringify([s.preset, s.vars, s.min, s.max, s.cooldownMin, s.graceMin]);

export type Box = number | string;
/** A number box as Vue gives it: '' when blank, which Number() would read as 0. */
const whole = (v: Box): boolean => v !== '' && Number.isInteger(Number(v)) && Number(v) >= 0;
/** Why the scaler's numbers cannot be saved as typed, or ''. The server stays the authority. */
export function scalerNumbersProblem(d: { min: Box; max: Box; cooldownMin: Box; graceMin: Box }): string {
  if (![d.min, d.max, d.cooldownMin, d.graceMin].every(whole)) return 'Whole numbers only.';
  return Number(d.max) < Number(d.min) ? 'Max below min.' : '';
}
