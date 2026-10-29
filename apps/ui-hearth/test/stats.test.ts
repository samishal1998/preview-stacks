import { expect, test } from 'bun:test';
import type { NodeStats, StackStats, Stats } from '@samyx/preview-stacks-client';
import { deploymentFor, diskUsedPct, fmtBytes, loadPerCore, memFreePct, neverReported, pressureLabel, sortStacks } from '../src/lib/stats';

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

// negative control: map cpu to 'Cpu' — the cpu case reads "Cpu pressure".
test('pressure badges name the kind, CPU in capitals', () => {
  expect(['disk', 'memory', 'cpu'].map(pressureLabel)).toEqual(['Disk pressure', 'Memory pressure', 'CPU pressure']);
});
