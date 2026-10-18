import { expect, test } from 'bun:test';
import { fileNameError, isPattern, targetOf } from '../src/lib/routing';

test('file names follow the server rule', () => {
  expect(fileNameError('mw.yml')).toBe('');
  expect(fileNameError('a.b-c.yaml')).toBe('');
  for (const bad of ['', 'Mw.yml', 'mw.toml', '../x.yml', 'a/b.yml', '.hid.yml', 'a..b.yml']) expect(fileNameError(bad)).not.toBe('');
});

test('route targets', () => {
  expect(targetOf({ target: '10.0.0.2:80' }).text).toBe('10.0.0.2:80');
  expect(targetOf({ target: null, targetReason: 'not-on-ingress' }).tone).toBe('danger');
  expect(targetOf({ target: null, targetReason: 'unknown-node' }).tone).toBe('info');
  expect(targetOf({ target: null }).text).toBe('not known');
  expect(isPattern('(pattern) x')).toBe(true);
});
