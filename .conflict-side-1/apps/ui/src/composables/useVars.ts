/**
 * Per-deployment variables, kept in this browser.
 *
 * THE SAFETY RULE ON THIS PAGE. A spec interpolates `${VAR}` exactly once, at resolve time, and
 * every route that resolves a deployment layers the request's query parameters over the server's
 * own environment. So `up` sent with `PR=7` and a later `down` sent without it resolve to two
 * DIFFERENT stacks, and the teardown quietly misses every container the deploy created — the exact
 * leak this project exists to prevent.
 *
 * Newer servers also STORE variables with the deployment (`vars` on the registry meta), which
 * makes the pair self-describing and is strictly better. This module does not replace that:
 *   - a request variable still OVERRIDES a stored one, so a wrong value typed here still retargets
 *     the stack — the warning stays true and stays visible;
 *   - a deployment submitted to an older server, or submitted without `vars`, has nothing stored,
 *     and then these pairs are the only thing keeping `up` and `down` matched.
 *
 * Keyed by deployment id. Corrupt or unavailable storage degrades to one empty row, never to a
 * thrown error — losing a convenience must not cost the page.
 */

import type { VarPair } from '../api/types';

const PREFIX = 'pstack.ui.vars.';

export function readVars(id: string): VarPair[] {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(PREFIX + id) ?? '[]');
    if (!Array.isArray(raw)) return [{ k: '', v: '' }];
    const pairs = raw
      .filter((e): e is { k: string; v?: unknown } => !!e && typeof (e as VarPair).k === 'string')
      .map((e) => ({ k: e.k, v: String(e.v ?? '') }));
    return pairs.length ? pairs : [{ k: '', v: '' }];
  } catch {
    return [{ k: '', v: '' }];
  }
}

export function writeVars(id: string, pairs: VarPair[]): void {
  if (!id) return;
  try {
    localStorage.setItem(PREFIX + id, JSON.stringify(pairs.filter((e) => e.k)));
  } catch {
    /* private mode / quota — the pairs still work for this page load */
  }
}

/** `{ PR: '7' }` → editor rows. Used to seed the editor from what the server has stored. */
export function pairsFromRecord(rec: Record<string, string> | undefined): VarPair[] {
  const pairs = Object.entries(rec ?? {}).map(([k, v]) => ({ k, v }));
  return pairs.length ? pairs : [{ k: '', v: '' }];
}

export function recordFromPairs(pairs: VarPair[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const e of pairs) if (e.k) out[e.k] = e.v;
  return out;
}

/**
 * Does the request-time set disagree with what the server stored? That is the dangerous shape:
 * both resolve, so nothing errors — they just resolve to different stacks.
 */
export function conflictingVars(
  pairs: VarPair[],
  stored: Record<string, string> | undefined,
): string[] {
  if (!stored) return [];
  return pairs.filter((e) => e.k && stored[e.k] !== undefined && stored[e.k] !== e.v).map((e) => e.k);
}
