/** Rules for the host settings page. Rendering hints only: the server's 403 is the enforcement. */
import { ROLES, type Role } from './users';

/** The server takes an integer of 1 or more. */
export function capProblem(draft: string | number | null): string {
  const raw = String(draft ?? '').trim();
  if (!raw) return 'Enter a number.';
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 1) return 'A whole number, 1 or more.';
  return '';
}

/** Fails closed: an unknown `minRole` is read as admin, an unknown account role as none. */
export function mayWrite(minRole: string, role: string | undefined, root: boolean): boolean {
  if (root) return true;
  const need = (ROLES as readonly string[]).includes(minRole) ? ROLES.indexOf(minRole as Role) : ROLES.length - 1;
  const have = ROLES.indexOf((role ?? '') as Role);
  return have >= 0 && have >= need;
}

export const SOURCE_LABEL = { db: 'Set here', env: 'From the environment', default: 'Shipped default' } as const;

/** Appearance choices, shared by the settings page and the shell's popover. */
/** `custom` is pstack's own theme (src/theme.css); the rest are the library's palettes. */
export const palettes = [
  { value: 'custom', label: 'Pstack' },
  ...['sunset', 'ocean', 'forest', 'dusk', 'rose'].map((v) => ({ value: v, label: v[0]!.toUpperCase() + v.slice(1) })),
];
/** The custom theme sets its own sizes, so density only applies to the library palettes. */
export const hasDensity = (theme: string): boolean => theme !== 'custom';
export const densities = [
  { value: 'comfortable', label: 'Comfortable' },
  { value: 'compact', label: 'Compact' },
];

/** Bump to reset a default once for every browser; the stored `v` says which reset it has seen. */
export const PREFS_VERSION = 2;
export type Prefs = { v: number; theme: string; mode: 'dark' | 'light' | 'system'; density: 'comfortable' | 'compact' };

/**
 * Stored prefs over the defaults. Prefs from before v1 predate a real density choice, so it is dropped;
 * before v2, `sunset` was the default rather than a choice, so it moves to the custom theme.
 */
export function mergePrefs(raw: unknown): Prefs {
  const stored = raw && typeof raw === 'object' ? { ...(raw as Partial<Prefs>) } : {};
  if (!(Number(stored.v) >= 1)) delete stored.density;
  if (!(Number(stored.v) >= 2) && stored.theme === 'sunset') delete stored.theme;
  return { theme: 'custom', mode: 'system', density: 'compact', ...stored, v: PREFS_VERSION };
}
