/** Roles, last-admin and form rules for accounts. Pure: no Vue, no DOM. */
import type { User } from '@samyx/preview-stacks-client';

export const ROLES = ['viewer', 'developer', 'maintainer', 'admin'] as const;
export type Role = (typeof ROLES)[number];

export const ROLE_WHAT: Record<Role, string> = {
  viewer: 'reads everything',
  developer: 'deploys and tears down',
  maintainer: 'host configuration',
  admin: 'accounts and sign-on',
};

export const MIN_PASSWORD = 8;
export const LAST_ADMIN_WHY = 'The only admin. Promote another account first.';

export const sentence = (s: string): string => (s ? s[0]!.toUpperCase() + s.slice(1) : s);

/** 1..4 for the known roles, 0 for anything else (below viewer, as the server ranks it). */
export function rank(role: string | null | undefined): number {
  return ROLES.indexOf((role ?? '') as Role) + 1;
}

/** Rendering only: root passes everything; the server decides what is allowed. */
export function can(min: Role, who: { root: boolean; role?: string | null }): boolean {
  if (who.root) return true;
  const have = rank(who.role);
  return have > 0 && have >= rank(min);
}

/** The last admin cannot be demoted or deleted. Counts account rows, like the server. */
export function isLastAdmin(u: Pick<User, 'role'>, users: Array<Pick<User, 'role'>>): boolean {
  return u.role === 'admin' && users.filter((x) => x.role === 'admin').length === 1;
}

export const roleOptions = ROLES.map((r) => ({ value: r, label: `${sentence(r)} - ${ROLE_WHAT[r]}` }));

export function canCreateUser(f: { username: string; password: string }): boolean {
  return f.username.trim().length > 0 && f.password.length >= MIN_PASSWORD;
}

/** A role the build does not know still has to show in the picker. */
export function pickerOptions(current: string) {
  return ROLES.includes(current as Role) ? roleOptions : [{ value: current, label: sentence(current) }, ...roleOptions];
}

/** One line for a failed call. The server's own words carry the remedy (403 names the role). */
export function problem(r: { status: number; body: { error?: string } }, what: string): string {
  if (r.status === 0) return `Could not ${what}. The server did not answer.`;
  const why = r.body.error ?? `HTTP ${r.status}`;
  return `Could not ${what}. ${why}`;
}
