/** Pure rules for request-time variables. Storage lives in src/vars/store.ts. */
import type { VarPair } from './varformats';

export type { VarPair };

export const BLANK: VarPair = { k: '', v: '' };
const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** A name a spec's `${NAME}` can reference. Blank is not an error, just unfinished. */
export const validName = (k: string): boolean => !k || NAME.test(k);

/** An editor never shows zero rows. */
export const withBlank = (pairs: VarPair[]): VarPair[] => (pairs.length ? pairs : [{ ...BLANK }]);

/** `{ PR: '7' }` to editor rows. */
export const pairsFromRecord = (rec: Record<string, string> | undefined): VarPair[] =>
  withBlank(Object.entries(rec ?? {}).map(([k, v]) => ({ k, v })));

export function recordFromPairs(pairs: VarPair[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const e of pairs) if (e.k) out[e.k] = e.v;
  return out;
}

/** Rows as `?A=1&B=2`, or '' when there are none. */
export function varsQuery(pairs: VarPair[], extra: Record<string, string | number> = {}): string {
  const p = new URLSearchParams();
  for (const e of pairs) if (e.k) p.set(e.k, e.v ?? '');
  for (const [k, v] of Object.entries(extra)) p.set(k, String(v));
  const s = p.toString();
  return s ? `?${s}` : '';
}

/**
 * Names where the request value disagrees with the stored one. Both resolve, so nothing errors;
 * they just resolve to different stacks.
 */
export function conflictingVars(pairs: VarPair[], stored: Record<string, string> | undefined): string[] {
  if (!stored) return [];
  return pairs.filter((e) => e.k && stored[e.k] !== undefined && stored[e.k] !== e.v).map((e) => e.k);
}

/** Parse saved JSON back into rows. Anything corrupt degrades to one blank row. */
export function parseSaved(raw: string | null): VarPair[] {
  try {
    const v: unknown = JSON.parse(raw ?? '[]');
    if (!Array.isArray(v)) return [{ ...BLANK }];
    return withBlank(
      v
        .filter((e): e is { k: string; v?: unknown } => !!e && typeof (e as VarPair).k === 'string')
        .map((e) => ({ k: e.k, v: String(e.v ?? '') })),
    );
  } catch {
    return [{ ...BLANK }];
  }
}

export const serializePairs = (pairs: VarPair[]): string => JSON.stringify(pairs.filter((e) => e.k));
