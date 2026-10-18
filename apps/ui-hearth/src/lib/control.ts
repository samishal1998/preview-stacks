import type { Job } from '@samyx/preview-stacks-client';

const ROLES = ['viewer', 'developer', 'maintainer', 'admin'] as const;
export type RoleName = (typeof ROLES)[number];

/** Rendering only; the server decides. Root passes everything, an unknown role ranks below viewer. */
export function atLeast(root: boolean, role: string | null | undefined, min: RoleName): boolean {
  if (root) return true;
  const have = ROLES.indexOf((role ?? '') as RoleName) + 1;
  return have > 0 && have >= ROLES.indexOf(min) + 1;
}

/** 268435456 -> "256 MiB"; null -> "unlimited". */
export function mem(bytes: number | null): string {
  if (bytes === null) return 'unlimited';
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GiB`;
  return `${Math.round(bytes / 1024 ** 2)} MiB`;
}

export function daysLeft(notAfter: number, now = Date.now()): number {
  return Math.floor((notAfter - now) / 86_400_000);
}

/** The cutover is 00:00 UTC of its day; before it Loki still writes to the filesystem. */
export function storageBadge(s3: { cutover: string } | null, today = new Date().toISOString().slice(0, 10)): string {
  if (!s3) return 'Filesystem';
  return s3.cutover > today ? `S3 from ${s3.cutover}` : 'S3';
}

/** Why a domain cannot be added as typed, or ''. The server stays the authority. */
export function domainProblem(raw: string, existing: string[], primary: string): string {
  const d = raw.trim().toLowerCase();
  if (!d) return 'Type a domain.';
  if (d.includes('://') || d.includes('/') || /\s/.test(d)) return 'A hostname only.';
  if (d === primary || existing.includes(d)) return 'Already listed.';
  return '';
}

export type Range = { min: number; max: number };
/** Why an integer box is out of range, or ''. */
export function rangeProblem(v: number | null, r: Range): string {
  if (v === null || !Number.isInteger(v)) return 'A whole number.';
  return v < r.min || v > r.max ? `${r.min} to ${r.max}.` : '';
}

/**
 * The job that replaced a superseded one: the nearest newer job for the same stack in a
 * newest-first list. Null when it aged out or the list has not loaded.
 */
export function supersededBy(job: Job, jobs: Job[]): Job | null {
  const i = jobs.findIndex((j) => j.id === job.id);
  if (i < 0) return null;
  return jobs.slice(0, i).reverse().find((j) => j.stack === job.stack) ?? null;
}

export const isTerminal = (state: string): boolean => !['queued', 'running'].includes(state);
