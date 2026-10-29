/**
 * The Stats page's arithmetic and the pressure settings' rules, tested directly: this app has no
 * component harness, so what would sit inline in the `.vue` lives in `composables/stats.ts`.
 *
 * Run with `cd apps/ui && bun test test/stats.test.ts`.
 */
import { expect, test } from 'bun:test';
import type { NodeStats, StackStats, Stats } from '../src/api/types';
import {
  PRESSURE,
  deploymentFor,
  diskUsedPct,
  fmtBytes,
  historyPerCore,
  loadPerCore,
  memFreePct,
  neverReported,
  pressureLabel,
  pressureProblem,
  sortStacks,
} from '../src/composables/stats';

const node = (o: Partial<NodeStats> = {}): NodeStats => ({
  node: null,
  hostname: 'worker-1',
  role: null,
  at: 1,
  stale: false,
  cores: 8,
  load1: 1,
  load5: 4,
  memTotal: 1000,
  memAvailable: 250,
  diskTotal: 200,
  diskFree: 10,
  pressure: [],
  prune: null,
  history: [],
  ...o,
});

test('disk used', () => {
  expect(diskUsedPct(node())).toBe(95);
  expect(diskUsedPct(node({ diskFree: null }))).toBeNull();
  expect(diskUsedPct(node({ diskTotal: null }))).toBeNull();
  expect(diskUsedPct(node({ diskTotal: 0, diskFree: 0 }))).toBeNull();
});

test('memory free', () => {
  expect(memFreePct(node())).toBe(25);
  expect(memFreePct(node({ memAvailable: null }))).toBeNull();
  expect(memFreePct(node({ memTotal: null }))).toBeNull();
  expect(memFreePct(node({ memTotal: 0, memAvailable: 0 }))).toBeNull();
});

test('load per core', () => {
  expect(loadPerCore(node())).toBe(0.5);
  expect(loadPerCore(node({ load5: null }))).toBeNull();
  expect(loadPerCore(node({ cores: null }))).toBeNull();
  expect(loadPerCore(node({ cores: 0 }))).toBeNull();
  expect(loadPerCore(node({ load5: 0 }))).toBe(0);
});

// negative control: return the raw load5 values from historyPerCore, and the 0.5 peak fails.
test('history per core: the same scale as the cpu limit, or null when cores are unknown', () => {
  const history = [
    { at: 1, load5: 4, memAvailable: 0, diskFree: 0 },
    { at: 2, load5: 2, memAvailable: 0, diskFree: 0 },
  ];
  expect(historyPerCore(node({ history }))).toEqual([0.5, 0.25]);
  expect(historyPerCore(node({ history, cores: null }))).toBeNull();
  expect(historyPerCore(node({ history, cores: 0 }))).toBeNull();
  expect(historyPerCore(node())).toEqual([]);
});

test('bytes, decimal', () => {
  expect(fmtBytes(null)).toBe('—');
  expect(fmtBytes(0)).toBe('0 B');
  expect(fmtBytes(999)).toBe('999 B');
  expect(fmtBytes(1000)).toBe('1.0 kB');
  expect(fmtBytes(129394278)).toBe('129 MB');
  expect(fmtBytes(1610612736)).toBe('1.6 GB');
  expect(fmtBytes(105152176128)).toBe('105 GB');
  expect(fmtBytes(2e15)).toBe('2.0 PB');
});

test('stacks by memory, then name, without touching the input', () => {
  const s = (stack: string, mem: number): StackStats => ({ stack, mem, cpuPct: 0, containers: 1, nodes: [] });
  const input = [s('b', 1), s('c', 5), s('a', 1)];
  expect(sortStacks(input).map((x) => x.stack)).toEqual(['c', 'a', 'b']);
  expect(input.map((x) => x.stack)).toEqual(['b', 'c', 'a']);
  expect(sortStacks([])).toEqual([]);
});

test('deployment for a stack', () => {
  const deps = [
    { id: 'pending', stack: null },
    { id: 'pr-7', stack: 'pr-7' },
    { id: 'second', stack: 'pr-7' },
  ];
  expect(deploymentFor('pr-7', deps)).toBe('pr-7');
  expect(deploymentFor('pr-9', deps)).toBeNull();
  expect(deploymentFor('', deps)).toBeNull();
  expect(deploymentFor('pr-7', [])).toBeNull();
});

test('never reported: no nodes, or none ever sent a batch (NP-R13)', () => {
  const st = (nodes: NodeStats[]): Stats => ({ v: 1, at: 1, agent: false, nodes, stacks: [], prune: { lastAt: null, lastFreed: null } });
  expect(neverReported(st([]))).toBe(true);
  expect(neverReported(st([node({ at: null, stale: true })]))).toBe(true);
  // Stale with history: it reported once, so the page shows it stale, not "upgrade".
  expect(neverReported(st([node({ at: null, stale: true, history: [{ at: 1, load5: 1, memAvailable: 1, diskFree: 1 }] })]))).toBe(false);
  expect(neverReported(st([node({ at: 5, stale: true })]))).toBe(false);
  expect(neverReported(st([node({ at: null }), node()]))).toBe(false);
});

test('pressure rules: blank is not 0 (0 turns the guard off)', () => {
  const p = (k: string) => PRESSURE.find((x) => x.key === k)!;
  expect(PRESSURE.map((x) => x.key)).toEqual(['pressure_disk_pct', 'pressure_mem_pct', 'pressure_cpu_load', 'pressure_sleep_idle_min', 'image_prune_after_h']);
  for (const x of PRESSURE) {
    expect(pressureProblem(x, '')).not.toBe('');
    expect(pressureProblem(x, '  ')).not.toBe('');
    expect(pressureProblem(x, '-1')).not.toBe('');
    expect(pressureProblem(x, 'abc')).not.toBe('');
    expect(pressureProblem(x, '0')).toBe('');
    expect(pressureProblem(x, 0)).toBe('');
  }
  expect(pressureProblem(p('pressure_disk_pct'), 90)).toBe('');
  expect(pressureProblem(p('pressure_disk_pct'), '101')).not.toBe('');
  expect(pressureProblem(p('pressure_mem_pct'), '9.5')).not.toBe('');
  expect(pressureProblem(p('pressure_cpu_load'), '2.5')).toBe('');
  expect(pressureProblem(p('pressure_cpu_load'), '500')).toBe('');
  expect(pressureProblem(p('image_prune_after_h'), '1000')).toBe('');
  expect(pressureProblem(p('pressure_sleep_idle_min'), '1.5')).not.toBe('');
  // The server's bounds (settings.go wholeMax). negative control: max Infinity for either key fails this.
  expect(pressureProblem(p('pressure_sleep_idle_min'), '525600')).toBe('');
  expect(pressureProblem(p('pressure_sleep_idle_min'), '525601')).toBe('0 to 525600.');
  expect(pressureProblem(p('image_prune_after_h'), '87600')).toBe('');
  expect(pressureProblem(p('image_prune_after_h'), '87601')).toBe('0 to 87600.');
  expect(pressureProblem(p('pressure_disk_pct'), '101')).toBe('0 to 100.');
});

// negative control: map cpu to 'Cpu' — the cpu case reads "Cpu pressure".
test('pressure badges name the kind, CPU in capitals', () => {
  expect(['disk', 'memory', 'cpu'].map(pressureLabel)).toEqual(['Disk pressure', 'Memory pressure', 'CPU pressure']);
});
