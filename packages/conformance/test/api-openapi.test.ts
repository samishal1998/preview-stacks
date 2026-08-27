/**
 * `GET /api/openapi.yaml` and `GET /api/openapi.json` — the API describing itself.
 *
 * The Go tests prove the document covers every route. What only a real process shows is that the
 * SERVED bytes are the same document: that the YAML is the file rather than a re-serialisation of
 * it, that the JSON is the same content in the same order, and that both are reachable by a client
 * that has no token — which is the whole point, since a client generating against a spec does not
 * have one yet.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer, type Booted } from '../harness/server.ts';

/** No Authorization header. A spec a caller must already be authorised to read is useless to them. */
const get = (s: Booted, path: string) => fetch(`${s.base}${path}`);

describe('the API describes itself', () => {
  test('YAML and JSON are the same document, and neither needs a token', async () => {
    // negative control: serve the JSON from a second, hand-maintained file — the two drift on the
    // first route added to one of them, and the path counts below stop matching.
    const s = await bootServer({ tag: 'oas' });
    try {
      const y = await get(s, '/api/openapi.yaml');
      expect(y.status).toBe(200);
      expect(y.headers.get('content-type')).toContain('application/yaml');
      const yamlText = await y.text();

      const j = await get(s, '/api/openapi.json');
      expect(j.status).toBe(200);
      expect(j.headers.get('content-type')).toContain('application/json');
      const doc = (await j.json()) as { openapi: string; info: { title: string }; paths: Record<string, unknown> };

      expect(doc.openapi).toStartWith('3.');
      expect(doc.info.title).toBe('pstack control plane');

      // Every path key in the JSON appears in the YAML text, and the counts agree — the cheapest
      // check that one was produced from the other rather than kept beside it.
      const yamlPaths = [...yamlText.matchAll(/^ {2}(\/api\/[^:\s]*):\s*$/gm)].map((m) => m[1] ?? '');
      expect(yamlPaths.length).toBeGreaterThan(40);
      expect(Object.keys(doc.paths).sort()).toEqual(yamlPaths.sort());
    } finally {
      await s.stop();
    }
  }, 20_000);

  test('the YAML is the file itself — comments and key order survive', async () => {
    // negative control: round-trip the YAML through a parser and re-emit it. The comment below is
    // the first thing lost, and with it the reasoning a reader of the document needs.
    const s = await bootServer({ tag: 'oas-verbatim' });
    try {
      const text = await (await get(s, '/api/openapi.yaml')).text();
      expect(text).toStartWith('openapi: 3.0.3');
      expect(text).toContain('# The pstack control-plane API, as a document.');
      // `openapi` before `info` before `paths` — file order, not alphabetical.
      expect(text.indexOf('\ninfo:')).toBeGreaterThan(text.indexOf('openapi:'));
      expect(text.indexOf('\npaths:')).toBeGreaterThan(text.indexOf('\ninfo:'));
    } finally {
      await s.stop();
    }
  }, 20_000);

  test('the JSON preserves the document order, so a diff between versions is legible', async () => {
    // negative control: marshal through a plain Go map — key order becomes arbitrary (or sorted),
    // and every release produces a spurious whole-file diff for anyone tracking the spec.
    const s = await bootServer({ tag: 'oas-order' });
    try {
      const raw = await (await get(s, '/api/openapi.json')).text();
      expect(raw.indexOf('"openapi"')).toBeLessThan(raw.indexOf('"info"'));
      expect(raw.indexOf('"info"')).toBeLessThan(raw.indexOf('"paths"'));
      // The first path in the file is the first in the JSON.
      const doc = (await (await get(s, '/api/openapi.json')).json()) as { paths: Record<string, unknown> };
      expect(Object.keys(doc.paths)[0]).toBe('/api/deployments');
    } finally {
      await s.stop();
    }
  }, 20_000);

  test('it documents itself, and a wrong method is refused', async () => {
    // negative control: leave the two routes out of the document — they are then the only routes on
    // the host with no description, in the document whose job is to describe every route.
    const s = await bootServer({ tag: 'oas-self' });
    try {
      const doc = (await (await get(s, '/api/openapi.json')).json()) as { paths: Record<string, unknown> };
      expect(doc.paths['/api/openapi.yaml']).toBeDefined();
      expect(doc.paths['/api/openapi.json']).toBeDefined();

      const posted = await fetch(`${s.base}/api/openapi.json`, { method: 'POST' });
      expect(posted.status).toBe(405);
    } finally {
      await s.stop();
    }
  }, 20_000);
});
