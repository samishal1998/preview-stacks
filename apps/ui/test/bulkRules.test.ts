/**
 * What a bulk action sends and what it skips. The skip rules are the only thing standing between a
 * select-all and a shared stack's teardown, so they are proved here rather than in a browser.
 *
 * Run with `cd apps/ui && bun test test/bulkRules.test.ts`.
 */
import { describe, expect, test } from 'bun:test';
import type { DeploymentRow } from '../src/api/types';
import { skipReason } from '../src/composables/bulkRules';
import { jobTarget } from '../src/composables/useJobQueue';

function row(over: Partial<DeploymentRow> = {}): DeploymentRow {
  return {
    id: 'pr-1',
    kind: 'isolated',
    createdAt: 1,
    updatedAt: 1,
    stack: 'app-pr-1',
    busy: false,
    running: true,
    asleep: null,
    orchestrator: 'compose',
    ...over,
  };
}

describe('skipReason', () => {
  test('a shared deployment is never torn down in bulk', () => {
    // negative control: drop the shared check — a select-all tears down the stack every preview
    // depends on, without the typed confirmation its own page demands.
    expect(skipReason(row({ kind: 'shared' }), 'down')).toContain('shared');
    expect(skipReason(row({ kind: 'shared' }), 'down-forget')).toContain('shared');
    expect(skipReason(row({ kind: 'shared' }), 'sleep')).toContain('shared');
    expect(skipReason(row({ kind: 'shared' }), 'up')).toBe('');
  });

  test('an unresolved deployment is skipped for every action', () => {
    // negative control: check only `unresolved` — a row with no stack name is sent anyway.
    for (const a of ['up', 'verify', 'sleep', 'wake', 'down', 'down-forget'] as const) {
      expect(skipReason(row({ stack: null }), a)).toBe('needs variables');
    }
  });

  test('sleep and wake only where they mean something', () => {
    const asleep = { since: 1, reason: 'idle' } as unknown as DeploymentRow['asleep'];
    expect(skipReason(row({ asleep }), 'sleep')).toBe('already asleep');
    expect(skipReason(row(), 'wake')).toBe('not asleep');
    expect(skipReason(row({ asleep }), 'wake')).toBe('');
    expect(skipReason(row({ orchestrator: null }), 'sleep')).toBe('no compose section');
    expect(skipReason(row(), 'down')).toBe('');
  });
});

describe('jobTarget', () => {
  test('a deployment job links to its deployment, a control job to Control', () => {
    expect(jobTarget({ deployment: 'pr 1' })).toBe('/deployments/pr%201');
    expect(jobTarget({ deployment: null })).toBe('/control');
  });

  test('an older server, which names no deployment, gets no link', () => {
    // negative control: `return job.deployment ? … : '/control'` — every job from an older server
    // links to Control, deploys included.
    expect(jobTarget({} as { deployment: string | null })).toBe(null);
  });
});
