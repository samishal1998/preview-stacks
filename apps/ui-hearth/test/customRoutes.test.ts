import { expect, test } from 'bun:test';
import type { CustomRoute } from '../src/api-routing';
import { deploymentApi } from '../src/api-deployment';
import { DEPTHS, depthEditable, depthLabel, focusLost, hostItems, moveQuestion, needsMove, toDepth } from '../src/lib/customRoutes';
import { useCustomRoutes } from '../src/lib/use-custom-routes';

const held = { deployment: 'myapp-pr-122', stack: 'myapp-pr-122' };

test('a list row needs a move only when another deployment holds it', () => {
  expect(needsMove({ deployment: null, stack: null }, 'b')).toBeNull();
  expect(needsMove({ deployment: 'b', stack: 'sb' }, 'b')).toBeNull();
  expect(needsMove({ deployment: 'a', stack: 'sa' }, 'b')).toEqual({ deployment: 'a', stack: 'sa' });
});

test('a 409 needs a move only when it says who holds the route', () => {
  // A stale list: the row looked free, the PUT came back held.
  expect(needsMove({ error: 'checkout is assigned to myapp-pr-122', assignedTo: held }, 'b')).toEqual(held);
  // A host or router collision is also a 409, and is an error, not a move.
  expect(needsMove({ error: 'web-checkout.example.com is served by x', host: 'web-checkout.example.com' }, 'b')).toBeNull();
  expect(needsMove({ error: 'checkout exists' }, 'b')).toBeNull();
  expect(needsMove({ assignedTo: { deployment: 'b', stack: 'sb' } }, 'b')).toBeNull();
});

test('the move question', () => {
  expect(moveQuestion('checkout', held, 'myapp-pr-123')).toBe('Move checkout from myapp-pr-122 to myapp-pr-123?');
  // An unresolved holder has no stack: name it by its deployment id.
  expect(moveQuestion('checkout', { deployment: 'dep-x', stack: null }, 'myapp-pr-123')).toBe('Move checkout from dep-x to myapp-pr-123?');
});

test('plain hosts are links, wildcards are text', () => {
  expect(hostItems(['web-checkout.example.com', '*.web-checkout.example.com'])).toEqual([
    { host: 'web-checkout.example.com', href: 'https://web-checkout.example.com' },
    { host: '*.web-checkout.example.com', href: null },
  ]);
  expect(hostItems([])).toEqual([]);
});

// negative control: drop the blocked map from hostItems → the taken host is missing and this fails;
// give it an href → it links to another stack and this fails.
test('a blocked host is text naming who has it', () => {
  expect(
    hostItems(['api-checkout.example.com'], [{ host: 'web-checkout.example.com', by: { kind: 'deployment', name: 'checkout' } }]),
  ).toEqual([
    { host: 'api-checkout.example.com', href: 'https://api-checkout.example.com' },
    { host: 'web-checkout.example.com', href: null, takenBy: 'checkout' },
  ]);
});

// negative control: dropping the `getClientRects` clause fails the removed-row and closed-dialog
// expects; dropping the `:disabled` clause fails the disabled-button expect.
test('focus is lost on body, on a disabled control, or on an element with no box', () => {
  const el = (boxes: number, disabled = false) => ({ getClientRects: () => ({ length: boxes }), matches: () => disabled }) as unknown as Element;
  const body = el(1);
  expect(focusLost(null, body)).toBe(true);
  expect(focusLost(body, body)).toBe(true);
  expect(focusLost(el(0), body)).toBe(true); // the unassigned row's button, or the closed dialog's
  expect(focusLost(el(1, true), body)).toBe(true); // Assign, once the pick clears
  expect(focusLost(el(1), body)).toBe(false);
});

test('depth labels, and Off is sent as null', () => {
  expect(DEPTHS.map((o) => o.label)).toEqual(['Off', 'One level', 'Any depth']);
  expect([depthLabel(null), depthLabel(undefined), depthLabel('one'), depthLabel('any')]).toEqual(['Off', 'Off', 'One level', 'Any depth']);
  // negative control: returning v as-is sends '' for Off, which the server refuses with a 400.
  expect(DEPTHS.map((o) => toDepth(o.value))).toEqual([null, 'one', 'any']);
});

test('a service wildcard is editable by a writer unless the spec sets it', () => {
  expect(depthEditable('spec', true)).toBe(false);
  expect(depthEditable('host', true)).toBe(true);
  expect(depthEditable(null, true)).toBe(true);
  expect(depthEditable(null, false)).toBe(false);
});

type Sent = { method: string; url: string; body: unknown };
async function withFetch(answer: (s: Sent) => Response, run: (sent: Sent[]) => Promise<void>): Promise<void> {
  const g = globalThis as { fetch: typeof fetch };
  const f = g.fetch;
  const sent: Sent[] = [];
  g.fetch = (async (url: string, init: RequestInit) => {
    const s = { method: init.method ?? 'GET', url, body: init.body ? JSON.parse(String(init.body)) : undefined };
    sent.push(s);
    return answer(s);
  }) as unknown as typeof fetch;
  try {
    await run(sent);
  } finally {
    g.fetch = f;
  }
}

const route = (name: string, subdomains: CustomRoute['subdomains'] = null): CustomRoute =>
  ({ name, deployment: 'd1', stack: 's1', subdomains, hosts: [`web-${name}.example.com`], blocked: [], createdAt: 1, updatedAt: 1 });

// negative control: dropping the row replacement leaves `a` at Off and its hosts unchanged; this fails.
test('saving a route\'s subdomains PUTs the staged depth and replaces only that row', async () => {
  const saved = { ...route('a', 'any'), hosts: ['web-a.example.com', '*.web-a.example.com'] };
  await withFetch(
    () => new Response(JSON.stringify(saved), { status: 200 }),
    async (sent) => {
      const cr = useCustomRoutes();
      cr.routes.value = [route('a'), route('b')];
      cr.subPick.a = 'any';
      expect(cr.subStaged(cr.routes.value[0])).toBe(true);
      await cr.saveSubdomains('a');
      expect(sent).toEqual([{ method: 'PUT', url: '/api/custom-routes/a/subdomains', body: { depth: 'any' } }]);
      expect(cr.routes.value).toEqual([saved, route('b')]);
      expect(cr.subOf(cr.routes.value[0])).toBe('any');
      expect(cr.subStaged(cr.routes.value[0])).toBe(false);
    },
  );
});

test('a refused save keeps the pick staged and the row as it was', async () => {
  await withFetch(
    (s) => (s.method === 'PUT' ? new Response(JSON.stringify({ error: '*.web-a.example.com is served by x', host: '*.web-a.example.com' }), { status: 409 }) : new Response(JSON.stringify([route('a')]), { status: 200 })),
    async (sent) => {
      const cr = useCustomRoutes();
      cr.routes.value = [route('a')];
      cr.subPick.a = '';
      expect(cr.subStaged(cr.routes.value[0])).toBe(false); // Off on an Off route: nothing to save
      cr.subPick.a = 'one';
      await cr.saveSubdomains('a');
      expect(sent[0]).toEqual({ method: 'PUT', url: '/api/custom-routes/a/subdomains', body: { depth: 'one' } });
      expect(cr.routes.value[0].subdomains).toBeNull();
      expect(cr.subStaged(cr.routes.value[0])).toBe(true);
    },
  );
});

test('a deployment service\'s subdomains: GET and PUT on one path, Off as null', async () => {
  await withFetch(
    () => new Response('{}', { status: 200 }),
    async (sent) => {
      await deploymentApi.subdomains('pr 1');
      await deploymentApi.setSubdomains('pr 1', 'web', toDepth(''));
      expect(sent).toEqual([
        { method: 'GET', url: '/api/deployments/pr%201/subdomains', body: undefined },
        { method: 'PUT', url: '/api/deployments/pr%201/subdomains', body: { service: 'web', depth: null } },
      ]);
    },
  );
});
