import type { ScalerSettings } from '../api/types';

/**
 * What the scaler draft refills on: the saved fields only, so a dismissal or a poll does not wipe typing.
 * A tuple, not an array replacer: a replacer also filters nested keys, so vars would read as {}.
 */
export const scalerSavedKey = (s: ScalerSettings): string =>
  JSON.stringify([s.preset, s.vars, s.min, s.max, s.cooldownMin, s.graceMin]);
