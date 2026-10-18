import { describe, expect, test } from 'bun:test';
import { blocker, exampleSpec, filterSpecs, previewDomain, routeFiles, submitBody, usersOf, validName } from '../src/lib/specs';

const inline = { source: 'inline' as const, spec: 'x', compose: '', specName: '' };

describe('specs', () => {
  test('names follow the server rule', () => {
    expect(validName('pr-123')).toBe(true);
    for (const bad of ['', 'PR', '-a', 'a..b', 'a/b', 'a'.repeat(65)]) expect(validName(bad)).toBe(false);
  });
  test('blocker names the first problem', () => {
    expect(blocker('', inline)).toBe('Enter an id.');
    expect(blocker('Bad', inline)).toContain('lower case');
    expect(blocker('a', { ...inline, spec: '  ' })).toBe('Enter a spec.');
    expect(blocker('a', { ...inline, source: 'stored' })).toBe('Choose a spec.');
    expect(blocker('a', inline)).toBe('');
    expect(blocker('a', { ...inline, source: 'stored', specName: 's' })).toBe('');
  });
  test('body sends vars and env; compose only when set; stored sends specName only', () => {
    const v = [{ k: 'PR', v: '7' }, { k: '', v: 'x' }];
    expect(submitBody(inline, v)).toEqual({ vars: { PR: '7' }, env: { PR: '7' }, spec: 'x' });
    expect(submitBody({ ...inline, compose: 'c' }, v).compose).toBe('c');
    expect(submitBody({ ...inline, source: 'stored', specName: 's' }, [])).toEqual({ vars: {}, env: {}, specName: 's' });
  });
  test('preview domain drops the first label, not for localhost or IPs', () => {
    expect(previewDomain('ui.preview.example.com')).toBe('preview.example.com');
    expect(previewDomain('example.com')).toBe('example.com');
    expect(previewDomain('localhost')).toBe('preview.example.com');
    expect(previewDomain('192.168.1.5')).toBe('preview.example.com');
    expect(exampleSpec('d.io')).toContain('PREVIEW_DOMAIN: d.io');
  });
  test('files route by name', () => {
    expect(routeFiles([{ name: 'preview.yml', text: 'a' }, { name: 'docker-compose.yml', text: 'b' }])).toEqual({ spec: 'a', compose: 'b' });
  });
  test('users and search', () => {
    expect(usersOf([{ id: 'a', specName: 's' }, { id: 'b', specName: null }], 's')).toEqual(['a']);
    const s = [{ name: 'web', description: 'Node app' }, { name: 'db' }];
    expect(filterSpecs(s, 'node')).toEqual([s[0]]);
    expect(filterSpecs(s, ' ')).toEqual(s);
  });
});
