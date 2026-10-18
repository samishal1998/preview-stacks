import { expect, test } from 'bun:test';
import { TOKEN_KEY, clearToken, getToken, setToken, tokenShape } from '../src/lib/token';

const mem = () => {
  const m = new Map<string, string>();
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) };
};
const broken = {
  getItem: () => {
    throw new Error('blocked');
  },
  setItem: () => {
    throw new Error('blocked');
  },
  removeItem: () => {
    throw new Error('blocked');
  },
};

test('round trip and clear', () => {
  const s = mem();
  expect(getToken(s)).toBe('');
  setToken('abc', s);
  expect(s.getItem(TOKEN_KEY)).toBe('abc');
  expect(getToken(s)).toBe('abc');
  clearToken(s);
  expect(getToken(s)).toBe('');
});
test('throwing storage never throws', () => {
  expect(getToken(broken)).toBe('');
  expect(() => setToken('x', broken)).not.toThrow();
  expect(() => clearToken(broken)).not.toThrow();
  expect(getToken(undefined)).toBe('');
});
test('shape', () => {
  expect(tokenShape('pstack_pat_x')).toBe('Personal token.');
  expect(tokenShape('a'.repeat(48))).toBe('Machine token.');
  expect(tokenShape('xy')).toBe('2 characters, not a pstack token.');
});
