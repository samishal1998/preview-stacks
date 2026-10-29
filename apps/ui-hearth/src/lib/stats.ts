/**
 * Arithmetic for the Stats page. Every reading can be null (a stale or never-reported node), and a
 * null stays null here: unknown is never drawn as 0 or as pressure. Pressure itself is the server's
 * call (it applies the recovery margin); the page shows `node.pressure`, never recomputes it.
 */
import type { NodeStats, StackStats, Stats } from '@samyx/preview-stacks-client';

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
