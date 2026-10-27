import { expect, test } from 'bun:test';
import { exploreUrl, serviceQuery, showGrafana, stackQuery, stackServices } from '../src/composables/grafana';

/** What Loki does with a `=~` value: unquote the LogQL string, then match the whole label. */
const matches = (query: string, label: string): boolean => {
  const value = JSON.parse(query.slice(query.indexOf('"'), query.lastIndexOf('"') + 1)) as string;
  return new RegExp(`^(?:${value})$`).test(label);
};

test('the stack query lists its services and never matches a longer stack', () => {
  // negative control: dropping regex() from the stack let `a.b` match `aXb-web`; dropping the dedupe gave `(web|web)`.
  expect(stackQuery('a', ['web', 'api', 'web'])).toBe('{service_name=~"a-(web|api)"}');
  expect(matches(stackQuery('a', ['web']), 'a-web')).toBe(true);
  expect(matches(stackQuery('a', ['web']), 'a-b-web')).toBe(false);
  expect(matches(stackQuery('a.b', ['web']), 'aXb-web')).toBe(false);
});

test('dots and pluses are regex-escaped, then string-escaped', () => {
  // negative control: without quote() the text read `a\.b-(c\+d)`, a LogQL parse error.
  const q = stackQuery('a.b', ['c+d']);
  expect(q).toBe('{service_name=~"a\\\\.b-(c\\\\+d)"}');
  expect(matches(q, 'a.b-c+d')).toBe(true);
  expect(encodeURIComponent(q)).toBe('%7Bservice_name%3D~%22a%5C%5C.b-(c%5C%5C%2Bd)%22%7D');
  // What Grafana reads back out of the link is the same LogQL text, escapes intact.
  expect(JSON.parse(new URL(exploreUrl('https://g', q)).searchParams.get('panes')!).a.queries[0].expr).toBe(q);
});

test('the service query is an exact match, quoted but not regex-escaped', () => {
  // negative control: running regex() here turned `a.b-c+d` into `a\\.b-c\\+d`, which `=` matches literally.
  expect(serviceQuery('myapp-pr-1', 'web')).toBe('{service_name="myapp-pr-1-web"}');
  expect(serviceQuery('a.b', 'c+d')).toBe('{service_name="a.b-c+d"}');
});

test('the Explore URL', () => {
  // negative control: from = 'now-6h' as the default, and a kept trailing slash (`//explore`), each failed this.
  const url = exploreUrl('https://grafana.example.com/', '{service_name="a-web"}');
  const u = new URL(url);
  expect(u.origin + u.pathname).toBe('https://grafana.example.com/explore');
  expect(u.searchParams.get('schemaVersion')).toBe('1');
  expect(u.searchParams.get('orgId')).toBe('1');
  expect(JSON.parse(u.searchParams.get('panes')!)).toEqual({
    a: {
      datasource: 'loki',
      queries: [{ refId: 'A', expr: '{service_name="a-web"}', datasource: { type: 'loki', uid: 'loki' } }],
      range: { from: 'now-1h', to: 'now' },
    },
  });
  expect(url).toBe(
    'https://grafana.example.com/explore?schemaVersion=1&panes=%7B%22a%22%3A%7B%22datasource%22%3A%22loki%22%2C%22queries%22%3A%5B%7B%22refId%22%3A%22A%22%2C%22expr%22%3A%22%7Bservice_name%3D%5C%22a-web%5C%22%7D%22%2C%22datasource%22%3A%7B%22type%22%3A%22loki%22%2C%22uid%22%3A%22loki%22%7D%7D%5D%2C%22range%22%3A%7B%22from%22%3A%22now-1h%22%2C%22to%22%3A%22now%22%7D%7D%7D&orgId=1',
  );
  expect(JSON.parse(new URL(exploreUrl('https://g', 'q', 'now-24h')).searchParams.get('panes')!).a.range.from).toBe('now-24h');
});

test('the gate: Grafana present, developer and up, not a token-only session', () => {
  // negative control: dropping `!tokenOnly` failed the third line; dropping `developer` the second.
  expect(showGrafana('https://grafana.example.com', true, false)).toBe(true);
  expect(showGrafana('https://grafana.example.com', false, false)).toBe(false);
  expect(showGrafana('https://grafana.example.com', true, true)).toBe(false);
  expect(showGrafana('', true, false)).toBe(false);
  expect(showGrafana(null, true, false)).toBe(false);
});

test('a stack names its running, taskless and declared services, each once', () => {
  // negative control: dropping declaredServices from stackServices left a down stack with [], so no link.
  const down = { containers: [], declaredServices: ['web', 'api'] };
  expect(stackServices(down)).toEqual(['web', 'api']);
  // negative control: dropping the running containers lost `extra`, a service the compose doesn't declare.
  const up = { containers: [{ service: 'web' }, { service: 'web' }, { service: 'extra' }, { service: null }], services: ['web', 'idle'], declaredServices: ['web', 'api'] };
  expect(stackServices(up)).toEqual(['web', 'extra', 'idle', 'api']);
  // An older server sends neither list.
  expect(stackServices({ containers: [{ service: 'web' }] })).toEqual(['web']);
});
