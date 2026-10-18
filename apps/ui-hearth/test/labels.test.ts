import { expect, test } from 'bun:test';
import { byLabel } from '../src/lib/labels';

const items = [
  { id: 1, name: 'ci' },
  { id: 2, name: 'ci' },
  { id: 3, name: 'laptop' },
  { id: 4, name: '' },
];
test('names are ids; repeats get the key; empty falls back to the key', () => {
  const m = byLabel(items, (i) => i.name, (i) => String(i.id));
  expect([...m.keys()]).toEqual(['ci (1)', 'ci (2)', 'laptop', '4']);
  expect(m.get('laptop')?.id).toBe(3);
});
