/**
 * When an assign is a move, the question it asks, and which hosts are links.
 *
 * Run with `cd apps/ui && bun test test/customRoutes.test.ts`.
 */
import { expect, test } from 'bun:test';
import { depthEditable, depthLabel, focusLost, hostItems, moveQuestion, needsMove, toDepth } from '../src/composables/customRoutes';

const held = { deployment: 'myapp-pr-122', stack: 'myapp-pr-122' };

// negative control: `return h` without the `h.deployment !== target` check fails the second expect.
test('a list row needs a move only when another deployment holds it', () => {
  expect(needsMove({ deployment: null, stack: null }, 'b')).toBeNull();
  expect(needsMove({ deployment: 'b', stack: 'sb' }, 'b')).toBeNull();
  expect(needsMove({ deployment: 'a', stack: 'sa' }, 'b')).toEqual({ deployment: 'a', stack: 'sa' });
});

// negative control: treating any 409 body as a move (ignoring `assignedTo`) fails the collision expects.
test('a 409 needs a move only when it says who holds the route', () => {
  // A stale list: the row looked free, the PUT came back held.
  expect(needsMove({ error: 'checkout is assigned to myapp-pr-122', assignedTo: held }, 'b')).toEqual(held);
  // A host or router collision is also a 409, and is an error, not a move.
  expect(needsMove({ error: 'web-checkout.example.com is served by x', host: 'web-checkout.example.com' }, 'b')).toBeNull();
  expect(needsMove({ error: 'checkout exists' }, 'b')).toBeNull();
  expect(needsMove({ assignedTo: { deployment: 'b', stack: 'sb' } }, 'b')).toBeNull();
});

// negative control: dropping the `?? from.deployment` fallback prints "from null" and fails the second expect.
test('the move question', () => {
  expect(moveQuestion('checkout', held, 'myapp-pr-123')).toBe('Move checkout from myapp-pr-122 to myapp-pr-123?');
  // An unresolved holder has no stack: name it by its deployment id.
  expect(moveQuestion('checkout', { deployment: 'dep-x', stack: null }, 'myapp-pr-123')).toBe('Move checkout from dep-x to myapp-pr-123?');
});

// negative control: linking every host fails on the `*.` entry.
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

// negative control: mapping null to '' wrongly (or dropping an option) fails the Off/Any depth expects.
test('depth labels', () => {
  expect(depthLabel(null)).toBe('Off');
  expect(depthLabel('one')).toBe('One level');
  expect(depthLabel('any')).toBe('Any depth');
});

// negative control: sending '' as the depth (not null) fails the first expect; the server 400s on it.
test('a select value becomes the PUT depth', () => {
  expect(toDepth('')).toBeNull();
  expect(toDepth('one')).toBe('one');
  expect(toDepth('any')).toBe('any');
  expect(toDepth('bogus')).toBeNull();
});

// negative control: ignoring `from` fails the spec expect; ignoring the role fails the viewer one.
test('a service wildcard is editable by a writer unless the spec sets it', () => {
  expect(depthEditable('spec', true)).toBe(false);
  expect(depthEditable('host', false)).toBe(false);
  expect(depthEditable('host', true)).toBe(true);
  expect(depthEditable(null, true)).toBe(true);
});
