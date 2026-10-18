import { expect, test } from 'bun:test';
import { importPlan, previewCounts, secretPairs, validName, type Entry } from '../src/lib/hostcfg';
import { formatVars, parseVars } from '../src/lib/varformats';

test('names', () => {
  expect(validName('DB_PASSWORD')).toBe(true);
  expect(validName('1A')).toBe(false);
  expect(validName('a-b')).toBe(false);
});

test('secrets export names only; import skips empties', () => {
  const es: Entry[] = [{ name: 'S', value: null, secret: true, updatedAt: 1 }, { name: 'V', value: 'x', secret: false, updatedAt: 1 }];
  expect(secretPairs(es)).toEqual([{ k: 'S', v: '' }]);
  expect(importPlan([{ k: 'A', v: '1' }, { k: 'B', v: '' }])).toEqual({ use: [{ k: 'A', v: '1' }], skipped: ['B'] });
});

test('env round trip', () => {
  const out = formatVars([{ k: 'A', v: 'one two' }], 'env');
  expect(parseVars(out).pairs).toEqual([{ k: 'A', v: 'one two' }]);
});

test('preview counts read old boolean sso', () => {
  const p = { preview: true as const, trusts: [], users: 1, tokens: 0, vars: 2, notifiers: 0, registries: 1, routing: 0, specs: 0, sso: true };
  const c = previewCounts(p);
  expect(c.find((x) => x.key === 'sso')?.value).toBe('1 provider');
  expect(c.find((x) => x.key === 'registries')?.value).toBe('1 registry');
});
