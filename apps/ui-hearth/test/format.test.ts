import { expect, test } from 'bun:test';
import { actionLabel, ago, jobTone, sentence, took } from '../src/format';
import { runState, summarize, filterDeployments } from '../src/lib/deployments';
import { addLine, countUnverifiable, isTerminal, jobTarget, leakedAxes, logEntries, stepLine, stepState, supersededBy, waitReason } from '../src/lib/jobs';
import { problem } from '../src/lib/problem';
import type { DeploymentRow, Job, StepResult } from '@samyx/preview-stacks-client';

test('ago and took', () => {
  expect(ago(null)).toBe('—');
  expect(ago(1000, 1000 + 5 * 60_000)).toBe('5m ago');
  expect(took(null, null)).toBe('—');
  expect(took(0 + 1000, 1000 + 187_000)).toBe('3m 07s');
  expect(took(1000, undefined, 1000 + 12_000)).toBe('12s…');
});
test('labels', () => {
  expect(actionLabel('down')).toBe('Tear down');
  expect(actionLabel(undefined)).toBe('—');
  expect(sentence('start-failed')).toBe('Start failed');
  expect(jobTone('leaked')).toBe('danger');
});

const dep = (o: Partial<DeploymentRow>): DeploymentRow => ({ id: 'a', kind: 'isolated', stack: 's', busy: false, running: false, ...o });
test('unknown is not idle and not zero', () => {
  expect(runState(dep({ busy: null }))).toBe('unknown');
  expect(runState(dep({ running: null }))).toBe('unknown');
  expect(runState(dep({}))).toBe('idle');
  expect(runState(dep({ busy: true }))).toBe('busy');
  const s = summarize([dep({ busy: null, running: null }), dep({ running: true, kind: 'shared' })]);
  expect([s.total, s.running, s.unknown, s.shared]).toEqual([2, 1, 1, 1]);
});
test('filters', () => {
  const list = [dep({ id: 'pr-1', specName: 'web' }), dep({ id: 'x', kind: 'shared', running: true })];
  expect(filterDeployments(list, { q: 'WEB', kinds: [], states: [] }).length).toBe(1);
  expect(filterDeployments(list, { q: '', kinds: ['shared'], states: ['running'] }).map((d) => d.id)).toEqual(['x']);
});

const job = (o: Partial<Job>): Job => ({ id: 'j', stack: 's', deployment: 'a', action: 'up', state: 'queued', startedAt: null, ...o }) as Job;
test('terminal is not "not running"', () => {
  expect(isTerminal('queued')).toBe(false);
  expect(isTerminal('running')).toBe(false);
  expect(isTerminal('superseded')).toBe(true);
});
test('wait reason and replacement', () => {
  const q = job({ id: 'q' });
  const run = job({ id: 'r', state: 'running' });
  expect(waitReason(q, [q, run])).toEqual({ kind: 'stack', blocker: run });
  expect(waitReason(q, [q, job({ id: 'o', stack: 'other', state: 'running' })])).toEqual({ kind: 'slot', running: 1 });
  expect(waitReason(run, [run]).kind).toBe('unknown');
  const old = job({ id: 'old', state: 'superseded' });
  const win = job({ id: 'win' });
  expect(supersededBy(old, [win, old])?.id).toBe('win');
  expect(supersededBy(old, [old])).toBeNull();
});
test('job target', () => {
  expect(jobTarget({ deployment: 'a b' })).toBe('/deployments/a%20b');
  expect(jobTarget({ deployment: null })).toBe('/control');
  expect(jobTarget({} as Job)).toBeNull();
});
const st = (o: Partial<StepResult>): StepResult => ({ axis: 'net', phase: 'up', ok: true, code: 0, skipped: false, ...o });
test('step states keep unverifiable and leaked apart', () => {
  expect(stepState(st({ message: 'unverifiable: no assert_gone', skipped: true }))).toBe('unverifiable');
  expect(stepState(st({ phase: 'assert_gone', ok: false, code: 1 }))).toBe('leaked');
  expect(stepState(st({ ok: false, code: 1 }))).toBe('failed');
  const steps = [st({ phase: 'assert_gone', ok: false, axis: 'vol' }), st({ message: 'unverifiable' })];
  expect(leakedAxes(steps)).toEqual(['vol']);
  expect(countUnverifiable(steps)).toBe(1);
});
test('log lines dedupe by seq and show the date once per day', () => {
  const at = new Date(2026, 0, 1, 10, 0, 0).getTime();
  const l = [{ seq: 1, at, level: 'info', message: 'a' }];
  expect(addLine(l, { seq: 1, at, level: 'info', message: 'a' })).toBe(l);
  expect(addLine(l, { seq: 2, at, level: 'warn', message: 'b' }).length).toBe(2);
  const e = logEntries([...l, { seq: 2, at: at + 1000, level: 'warn', message: 'b' }, { seq: 3, at: at + 86_400_000, level: 'error', message: 'c' }]);
  expect(e[0]!.timestamp).toContain('2026-01-01');
  expect(e[1]!.timestamp).toBe('10:00:01');
  expect(e[1]!.level).toBe('warning');
  expect(e[2]!.timestamp).toContain('2026-01-02');
});
test('problem sentences', () => {
  expect(problem({ status: 409, body: { error: 'busy' } }, 'Deploy')).toBe('Deploy: busy');
  expect(problem({ status: 403, body: {} }, 'Deploy')).toBe('Deploy: not allowed.');
  expect(problem({ status: 500, body: {} }, 'Deploy')).toBe('Deploy: HTTP 500');
  expect(problem({ status: 0, body: { error: 'x' } }, 'Deploy')).toBe('x');
});

test('a step line never repeats its state', () => {
  expect(stepLine(st({}))).toBe('ok');
  expect(stepLine(st({ ok: false, code: 1 }))).toBe('failed: exit 1');
  expect(stepLine(st({ message: 'unverifiable: no assert_gone', skipped: true }))).toBe('unverifiable: no assert_gone');
});
