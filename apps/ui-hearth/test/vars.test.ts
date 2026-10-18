import { describe, expect, test } from 'bun:test';
import { conflictingVars, pairsFromRecord, parseSaved, recordFromPairs, serializePairs, validName, varsQuery } from '../src/lib/vars';

describe('vars', () => {
  test('query encodes and skips blank names', () => {
    expect(varsQuery([{ k: 'PR', v: '7' }, { k: '', v: 'x' }, { k: 'A', v: 'a b&c' }])).toBe('?PR=7&A=a+b%26c');
    expect(varsQuery([])).toBe('');
    expect(varsQuery([{ k: 'PR', v: '7' }], { tail: 5 })).toBe('?PR=7&tail=5');
  });
  test('record round trip, blank row when empty', () => {
    expect(pairsFromRecord(undefined)).toEqual([{ k: '', v: '' }]);
    expect(recordFromPairs(pairsFromRecord({ A: '1', B: '' }))).toEqual({ A: '1', B: '' });
  });
  test('conflicts are names whose values differ', () => {
    expect(conflictingVars([{ k: 'PR', v: '8' }, { k: 'X', v: '1' }, { k: 'Z', v: 'z' }], { PR: '7', Z: 'z' })).toEqual(['PR']);
    expect(conflictingVars([{ k: 'PR', v: '8' }], undefined)).toEqual([]);
  });
  test('saved rows survive, corrupt input degrades', () => {
    expect(parseSaved(serializePairs([{ k: 'A', v: '1' }, { k: '', v: '' }]))).toEqual([{ k: 'A', v: '1' }]);
    for (const bad of [null, '{', '{"a":1}', '[]']) expect(parseSaved(bad)).toEqual([{ k: '', v: '' }]);
    expect(parseSaved('[{"k":"A"},5,null]')).toEqual([{ k: 'A', v: '' }]);
  });
  test('names', () => {
    expect(validName('')).toBe(true);
    expect(validName('PR_1')).toBe(true);
    expect(validName('foo-bar')).toBe(false);
    expect(validName('1a')).toBe(false);
  });
});
