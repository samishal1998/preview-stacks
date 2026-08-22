/**
 * The vacuity control: a server that answers `200 {}` to every request.
 *
 * A conformance test that passes against THIS asserts nothing about pstack. `scripts/vacuity.ts`
 * runs the whole suite in `PSTACK_IMPL=null` and reports every test that did not fail.
 */
export function startNullServer(port: number): () => void {
  const s = Bun.serve({
    port,
    hostname: '127.0.0.1',
    fetch: () => new Response('{}', { headers: { 'content-type': 'application/json' } }),
  });
  return () => s.stop(true);
}
