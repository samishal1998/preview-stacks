import { expect, test } from 'bun:test';
import type { Runtime } from '@samyx/preview-stacks-client';
import { deploymentApi } from '../src/api-deployment';
import { writeVars } from '../src/vars/store';
import {
  capLines,
  classifyConflict,
  containerState,
  containersWithServices,
  curlFor,
  detailNote,
  filterLines,
  logFileName,
  logLevel,
  parseLogLine,
  problemText,
  routeTarget,
  runState,
  shareViews,
  sortFindings,
  taskOf,
  urlFor,
  whyDisabled,
} from '../src/lib/deployment';

const dep = { kind: 'isolated' as const, busy: false, asleep: null, orchestrator: 'compose' as const, stack: 'pr-1' };

test('problemText', () => {
  expect(problemText(403, 'x', 'Share')).toBe('Share: not allowed for your role.');
  expect(problemText(404, undefined, 'Logs')).toBe('Logs: not found.');
  expect(problemText(500, 'boom', 'Logs')).toBe('Logs: boom');
  expect(problemText(0, undefined, 'Logs')).toBe('The server did not answer.');
});

test('a failed poll is a server problem, not an unresolved spec', () => {
  // negative control: treating 5xx like 400 gives this 'Could not resolve' and an Open Config button.
  expect(detailNote(500, true)).toEqual({ title: 'Could not refresh this deployment', config: false });
  expect(detailNote(0, false)).toEqual({ title: 'Could not load this deployment', config: false });
  expect(detailNote(400, false)).toEqual({ title: 'Could not resolve this deployment', config: true });
  expect(detailNote(403, false).config).toBe(true);
});

test('Run again sends the saved variables, like Deploy', async () => {
  // negative control: drop vq(d) from deploymentApi.act and the request has no query; this fails. JobView and the bulk actions call it; api.act (no vars) is gone.
  const g = globalThis as { localStorage?: unknown; fetch: typeof fetch };
  const [ls, f] = [g.localStorage, g.fetch];
  const m = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) };
  const urls: string[] = [];
  g.fetch = (async (u: string) => {
    urls.push(u);
    return new Response('{}', { status: 202 });
  }) as unknown as typeof fetch;
  try {
    writeVars('pr-1', [{ k: 'PR', v: '7' }]);
    await deploymentApi.act('pr-1', 'up');
    expect(urls).toEqual(['/api/deployments/pr-1/up?PR=7']);
  } finally {
    g.localStorage = ls;
    g.fetch = f;
  }
});

test('conflicts are read by shape', () => {
  expect(classifyConflict({ error: 'e', kind: 'shared' }).kind).toBe('shared');
  expect(classifyConflict({ error: 'e', containers: 2 }).kind).toBe('containers');
  expect(classifyConflict({ error: 'e', deployments: ['a'] }).deployments).toEqual(['a']);
  expect(classifyConflict({ error: 'a job is running' }).kind).toBe('other');
});

test('runState keeps unknown apart from idle', () => {
  expect(runState({ busy: null, running: null }).label).toBe('Unknown');
  expect(runState({ busy: false, running: false }).label).toBe('Idle');
  expect(runState({ busy: true, running: true }).label).toBe('Busy');
  expect(runState({ busy: false, running: false, asleep: { since: 1, reason: 'idle', hosts: [], rules: [] } }).label).toBe('Asleep');
});

test('whyDisabled', () => {
  expect(whyDisabled('up', null)).toBe('Needs variables.');
  expect(whyDisabled('up', { ...dep, busy: true })).toContain('running or waiting');
  expect(whyDisabled('down', { ...dep, busy: true })).toBe('');
  expect(whyDisabled('down', { ...dep, kind: 'shared' })).toContain('Type pr-1');
  expect(whyDisabled('down', { ...dep, kind: 'shared' }, true)).toBe('');
  expect(whyDisabled('sleep', { ...dep, kind: 'shared' })).toBe('Shared stacks do not sleep.');
  expect(whyDisabled('sleep', { ...dep, orchestrator: null })).toBe('No compose section.');
});

test('log lines', () => {
  expect(parseLogLine('web-1  | 2026-01-02T03:04:05.123Z hello')).toEqual({ service: 'web-1', time: '2026-01-02T03:04:05.123Z', message: 'hello' });
  expect(parseLogLine('plain')).toEqual({ message: 'plain' });
  expect(logLevel('FATAL: x')).toBe('error');
  expect(logLevel('a warning here')).toBe('warning');
  expect(logLevel('ok')).toBe('info');
  expect(filterLines(['Alpha', 'beta'], ' ALP ')).toEqual(['Alpha']);
  expect(filterLines(['a'], '')).toEqual(['a']);
  expect(capLines([1, 2, 3], 2)).toEqual([2, 3]);
  expect(logFileName('pr/1', 'web', new Date('2026-01-02T03:04:05Z'))).toBe('pr_1-web-20260102030405.log');
});

test('task names and urls', () => {
  expect(taskOf('stack_app.1.abc')).toBe('1.abc');
  expect(taskOf('stack-app-1')).toBe('stack-app-1');
  const r = { routes: [{ container: 'app', hosts: ['(pattern) x', 'a.example.com'] }] } as Runtime;
  expect(urlFor(r, { name: 'stack_app.1.x', service: 'app' })).toBe('https://a.example.com');
  expect(urlFor(r, { name: 'db', service: 'db' })).toBeNull();
});

test('routeTarget says why there is no address', () => {
  expect(routeTarget({ target: '10.0.0.2:80' }).text).toBe('10.0.0.2:80');
  expect(routeTarget({ target: null, targetReason: 'not-on-ingress' }).tone).toBe('danger');
  expect(routeTarget({ target: null, targetReason: 'no-port' }).tone).toBe('warning');
  expect(routeTarget({ target: null }).text).toBe('Not known');
});

test('containers: state, taskless services', () => {
  expect(containerState({ state: 'exited', health: 'healthy' }).label).toBe('Exited');
  expect(containerState({ state: 'running', health: 'unhealthy' }).tone).toBe('danger');
  const r = { containers: [{ id: 'c1', service: 'a', name: 'a-1' }], services: ['a', 'b'] } as Runtime;
  expect(containersWithServices(r).map((c) => c.id)).toEqual(['c1', 'service:b']);
});

test('findings sort worst first', () => {
  const f = sortFindings([
    { level: 'info', message: 'i' },
    { level: 'error', message: 'e' },
    { level: 'warn', message: 'w' },
  ]);
  expect(f.map((x) => x.level)).toEqual(['error', 'warn', 'info']);
});

test('share + curl', () => {
  expect(shareViews(true, false)).toEqual(['details']);
  expect(shareViews(false, false)).toEqual([]);
  const c = curlFor('https://h', 'POST', '/api/x', { a: "it's" });
  expect(c).toContain('$PSTACK_TOKEN');
  expect(c).toContain(`it'\\''s`);
  expect(curlFor('https://h', 'GET', '/api/x')).not.toContain('-d');
});
