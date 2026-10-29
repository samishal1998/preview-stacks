/**
 * Arithmetic for the Stats page, and the rules for the five pressure settings.
 *
 * Every reading can be null (a stale or never-reported node), and a null stays null here: unknown
 * is never drawn as 0 or as pressure. Pressure itself is the server's call (it applies the recovery
 * margin); the page shows `node.pressure`, never recomputes it. Same names and null rules as the
 * Hearth UI's `lib/stats.ts` and `lib/settings.ts`.
 */
import type { NodeStats, SettingKey, StackStats, Stats } from '../api/types';

const ratio = (part: number | null, whole: number | null): number | null =>
  part === null || whole === null || whole <= 0 ? null : part / whole;

export function diskUsedPct(n: NodeStats): number | null {
  const free = ratio(n.diskFree, n.diskTotal);
  return free === null ? null : 100 - free * 100;
}

export function memFreePct(n: NodeStats): number | null {
  const free = ratio(n.memAvailable, n.memTotal);
  return free === null ? null : free * 100;
}

/** The 5-minute load over cores, what `pressure_cpu_load` is compared with. */
export const loadPerCore = (n: NodeStats): number | null => ratio(n.load5, n.cores);

/** A pressure kind as a badge says it: `cpu` is an acronym, so not `sentence()`. */
export const pressureLabel = (kind: string): string =>
  `${({ disk: 'Disk', memory: 'Memory', cpu: 'CPU' } as Record<string, string>)[kind] ?? kind} pressure`;

/** The hour of load5 samples per core, the scale its limit is on; null when cores are unknown. */
export const historyPerCore = (n: NodeStats): number[] | null =>
  n.cores === null || n.cores <= 0 ? null : n.history.map((h) => h.load5 / (n.cores as number));

const UNITS = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];

/** Decimal units, like `docker system df`: "1.6 GB", "129 MB". */
export function fmtBytes(b: number | null): string {
  if (b === null) return '—';
  let i = 0;
  while (b >= 1000 && i < UNITS.length - 1) {
    b /= 1000;
    i++;
  }
  return i === 0 ? `${b} B` : `${b < 10 ? b.toFixed(1) : Math.round(b)} ${UNITS[i]}`;
}

export const sortStacks = (s: StackStats[]): StackStats[] =>
  [...s].sort((a, b) => b.mem - a.mem || (a.stack < b.stack ? -1 : a.stack > b.stack ? 1 : 0));

/** The first deployment that resolves to this stack; two may, and either is the right page. */
export const deploymentFor = (stack: string, deployments: ReadonlyArray<{ id: string; stack: string | null }>): string | null =>
  (stack && deployments.find((d) => d.stack === stack)?.id) || null;

/** No node has ever sent a batch: only then is "upgrade" the advice (NP-R13). A crashed agent is stale, not missing. */
export const neverReported = (s: Stats): boolean => s.nodes.every((n) => n.at === null && n.history.length === 0);

/** The node-pressure keys, in page order. `0` turns each off. */
export const PRESSURE = [
  { key: 'pressure_disk_pct', label: 'Disk pressure at', unit: '% used', max: 100, int: true },
  { key: 'pressure_mem_pct', label: 'Memory pressure at', unit: '% free', max: 100, int: true },
  { key: 'pressure_cpu_load', label: 'CPU pressure at', unit: '5-min load per core', max: Infinity, int: false },
  { key: 'pressure_sleep_idle_min', label: 'Auto-sleep after', unit: 'Minutes idle', max: 525600, int: true },
  { key: 'image_prune_after_h', label: 'Prune images older than', unit: 'Hours', max: 87600, int: true },
] as const satisfies ReadonlyArray<{ key: SettingKey; label: string; unit: string; max: number; int: boolean }>;
export type PressureSetting = (typeof PRESSURE)[number];

/**
 * Why a draft cannot be saved, or ''. A blank field is a problem, not 0: 0 would turn the guard off.
 * Takes a number too — `v-model` on `type="number"` hands one back once the value parses.
 */
export function pressureProblem(p: PressureSetting, draft: string | number): string {
  const raw = String(draft).trim();
  if (!raw) return 'Enter a number.';
  const n = Number(raw);
  if (!Number.isFinite(n) || n < 0 || n > p.max) return p.max === Infinity ? '0 or more.' : `0 to ${p.max}.`;
  if (p.int && !Number.isInteger(n)) return 'A whole number.';
  return '';
}
