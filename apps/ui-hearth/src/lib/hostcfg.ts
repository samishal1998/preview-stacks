/** Pure rules for host variables and the sealed-config preview. No Vue, no DOM. */
import type { VarPair } from './varformats';

export type Entry = { name: string; value: string | null; secret: boolean; updatedAt: number };

export const NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
export const validName = (n: string): boolean => NAME_RE.test(n);

/** Secrets export names only: the API never returns a value, and a mask would deploy as a real one. */
export const secretPairs = (es: Entry[]): VarPair[] => es.filter((e) => e.secret).map((e) => ({ k: e.name, v: '' }));
export const varPairs = (es: Entry[]): VarPair[] => es.filter((e) => !e.secret).map((e) => ({ k: e.name, v: e.value ?? '' }));
export const WITHHELD = 'Values are never exported. Fill each one in.';

/** An import never stores an empty value (it would blank a live secret). Returns what to store and what was skipped. */
export function importPlan(pairs: VarPair[]): { use: VarPair[]; skipped: string[] } {
  return { use: pairs.filter((p) => p.v), skipped: pairs.filter((p) => !p.v).map((p) => p.k) };
}

export type Preview = {
  preview: true;
  trusts: string[];
  users: number;
  tokens: number;
  vars: number;
  notifiers: number;
  registries: number;
  routing: number;
  specs: number;
  /** A count on current servers, a boolean on older ones. */
  sso: number | boolean;
};
export type Applied = { trusts: string[]; created: string[]; skipped: string[] };

const plural = (n: number, one: string, many = `${one}s`): string => `${n} ${n === 1 ? one : many}`;

/** The counts line items, in display order. */
export function previewCounts(p: Preview): { key: string; label: string; value: string }[] {
  const sso = typeof p.sso === 'number' ? p.sso : p.sso ? 1 : 0;
  const rows = [
    ['users', 'Accounts', plural(p.users, 'account')],
    ['tokens', 'API tokens', plural(p.tokens, 'token')],
    ['vars', 'Variables', plural(p.vars, 'variable')],
    ['notifiers', 'Notifiers', plural(p.notifiers, 'notifier')],
    ['registries', 'Registries', plural(p.registries, 'registry', 'registries')],
    ['routing', 'Routing', plural(p.routing, 'file')],
    ['specs', 'Specs', plural(p.specs, 'spec')],
    ['sso', 'Sign-on', plural(sso, 'provider')],
  ] as const;
  return rows.map(([key, label, value]) => ({ key, label, value }));
}
