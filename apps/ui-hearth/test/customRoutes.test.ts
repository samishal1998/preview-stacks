import { expect, test } from 'bun:test';
import { hostItems, moveQuestion, needsMove } from '../src/lib/customRoutes';

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
