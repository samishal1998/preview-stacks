import { expect, test } from 'bun:test';
import type { SsoPreset } from '@samyx/preview-stacks-client';
import { assembled, blank, buildBody, cannotSave, list, safeNext, startAdd, suggestKey, templateParts } from '../src/lib/sso';

const okta = {
  key: 'okta', label: 'Okta', mode: 'oidc', buttonLabel: '', setupUrl: '', setupHint: '',
  discoveryUrl: 'https://<your-domain>.okta.com/', authorizeUrl: '', tokenUrl: '', userInfoUrl: '', scopes: 'openid',
  claimMap: {},
} as unknown as SsoPreset;

test('blank inherits the host role, never admin', () => {
  expect(blank().defaultRole).toBe('');
});

test('suggestKey finds a free slug', () => {
  expect(suggestKey('github', [])).toBe('github');
  expect(suggestKey('github', ['github', 'github-2'])).toBe('github-3');
});

test('template issuer is assembled from placeholders', () => {
  const e = startAdd({ preset: okta, mode: 'oidc' }, []);
  const parts = templateParts(e)!;
  expect(assembled(parts, {})).toBe('https://<your-domain>.okta.com/');
  expect(assembled(parts, { 'your-domain': ' acme ' })).toBe('https://acme.okta.com/');
  expect(e.form.discoveryUrl).toBe('');
});

test('cannotSave names the first missing thing', () => {
  const e = startAdd({ preset: null, mode: 'oidc' }, []);
  expect(cannotSave(e, [])).toContain('client id');
  e.form.clientId = 'c';
  expect(cannotSave(e, [])).toContain('secret');
  e.secret = 's';
  expect(cannotSave(e, [])).toContain('issuer');
  e.form.discoveryUrl = 'https://x.example';
  expect(cannotSave(e, [])).toBe('');
  expect(cannotSave(e, ['oidc'])).toContain('taken');
  e.key = 'Bad Key';
  expect(cannotSave(e, [])).toContain('slug');
});

test('editing keeps the stored secret when the field is empty', () => {
  const e = startAdd({ preset: null, mode: 'oidc' }, []);
  e.originalKey = 'oidc';
  e.secretSet = true;
  e.form.clientId = 'c';
  e.form.discoveryUrl = 'https://x.example';
  expect(cannotSave(e, ['oidc'])).toBe('');
  expect(buildBody(e).clientSecret).toBe('');
});

test('body splits the rule lists and drops groups for oidc', () => {
  const e = startAdd({ preset: null, mode: 'oidc' }, []);
  e.domains = 'a.com, ,b.com';
  e.groups = 'x';
  const b = buildBody(e);
  expect(b.allowedEmailDomains).toEqual(['a.com', 'b.com']);
  expect(b.requiredGroups).toEqual([]);
  expect(list('')).toEqual([]);
});

test('safeNext only lets same-origin paths through', () => {
  expect(safeNext('/deployments/a')).toBe('/deployments/a');
  expect(safeNext('//evil.example')).toBe('/');
  expect(safeNext('https://evil.example')).toBe('/');
  expect(safeNext('/login?next=/x')).toBe('/');
  expect(safeNext(undefined)).toBe('/');
});
