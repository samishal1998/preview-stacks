/** Request variables remembered per deployment in this browser. Never throws. */
import { parseSaved, serializePairs, type VarPair } from '../lib/vars';

const PREFIX = 'pstack.ui.vars.';

export function readVars(dep: string): VarPair[] {
  try {
    return parseSaved(localStorage.getItem(PREFIX + dep));
  } catch {
    return parseSaved(null);
  }
}

export function writeVars(dep: string, pairs: VarPair[]): void {
  if (!dep) return;
  try {
    localStorage.setItem(PREFIX + dep, serializePairs(pairs));
  } catch {
    /* private window: pairs still work for this page load */
  }
}
