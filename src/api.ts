/**
 * HTTP API + static UI host.
 *
 *   GET  /api/health                     liveness + whether auth is enforced
 *   GET  /api/spec?pr=123                the resolved spec for that stack
 *   GET  /api/stacks                     compose projects on this host (what actually exists)
 *   GET  /api/stacks/:id/status          compose ps for one stack
 *   POST /api/stacks/:id/up              → { job }
 *   POST /api/stacks/:id/down            → { job }   body: { verify?: boolean }
 *   POST /api/stacks/:id/verify          → { job }
 *   GET  /api/jobs                       recent job transcripts
 *   GET  /api/jobs/:jobId                one job (poll this for state)
 *   GET  /api/jobs/:jobId/stream         SSE live log
 *   GET  /                               the Vue UI
 *
 * `:id` is the value of the spec's stack variable (default `PR`), not the resolved stack name —
 * the server owns the spec and resolves `pr-${PR}` itself. That keeps stack naming in one place
 * and means a client cannot ask the server to act on an arbitrary compose project.
 *
 * SECURITY. This API destroys infrastructure. Two guards:
 *   1. A bearer token (`PSTACK_TOKEN`). Required for every mutating route.
 *   2. If no token is set the server binds to 127.0.0.1 and refuses to listen on 0.0.0.0, so an
 *      unauthenticated instance cannot be exposed by accident.
 * It is NOT multi-tenant: every caller shares one spec and one Docker socket. Put it behind your
 * ingress' auth (or an SSH tunnel) before letting anyone but you reach it.
 */

import { loadSpec, SpecError, type Stack } from './spec.ts';
import { createRunner } from './exec.ts';
import { down, status, up, verify } from './stack.ts';
import { JobRegistry } from './jobs.ts';

export type ServerOptions = {
  specPath: string;
  /** Spec variable bound to the `:id` path segment. */
  varName: string;
  port: number;
  host: string;
  token?: string;
  uiDir: string;
};

const json = (body: unknown, init: ResponseInit = {}) =>
  new Response(JSON.stringify(body, null, 2), {
    ...init,
    headers: { 'content-type': 'application/json', ...(init.headers ?? {}) },
  });

export function createServer(opts: ServerOptions) {
  const jobs = new JobRegistry();

  /** Re-resolve the spec per request with `:id` bound to the configured variable. */
  const specFor = async (id: string): Promise<Stack> =>
    loadSpec(opts.specPath, { ...process.env, [opts.varName]: id });

  const runnerFor = (spec: Stack) =>
    createRunner({ dryRun: false, level: 'quiet', baseEnv: { ...process.env, ...spec.env } as Record<string, string> });

  const authed = (req: Request): boolean => {
    if (!opts.token) return true; // localhost-only mode; see the header note
    const h = req.headers.get('authorization') ?? '';
    const m = /^Bearer (.+)$/.exec(h);
    // Constant-time-ish: compare lengths first, then content. Not a hardened comparison — the
    // threat model here is "don't leave it wide open", not resisting a timing oracle.
    return !!m && m[1]!.length === opts.token.length && m[1] === opts.token;
  };

  return Bun.serve({
    port: opts.port,
    hostname: opts.host,
    idleTimeout: 240, // SSE streams must outlive the default
    async fetch(req) {
      const url = new URL(req.url);
      const path = url.pathname;

      // ---- static UI ----------------------------------------------------------------
      if (!path.startsWith('/api/')) {
        const rel = path === '/' ? 'index.html' : path.replace(/^\/+/, '');
        // Contain traversal: resolve and require the result to stay under uiDir.
        const full = `${opts.uiDir}/${rel}`;
        if (!full.startsWith(opts.uiDir) || rel.includes('..')) return new Response('no', { status: 400 });
        const file = Bun.file(full);
        if (await file.exists()) return new Response(file);
        return new Response('not found', { status: 404 });
      }

      if (path === '/api/health') {
        return json({ ok: true, authEnforced: !!opts.token, spec: opts.specPath, varName: opts.varName });
      }

      const mutating = req.method === 'POST' || req.method === 'DELETE';
      if (mutating && !authed(req)) return json({ error: 'unauthorized' }, { status: 401 });

      try {
        // ---- spec -------------------------------------------------------------------
        if (path === '/api/spec') {
          const id = url.searchParams.get(opts.varName.toLowerCase()) ?? url.searchParams.get('id');
          if (!id) return json({ error: `missing ?${opts.varName.toLowerCase()}=` }, { status: 400 });
          const spec = await specFor(id);
          return json({
            stack: spec.stack,
            compose: spec.compose,
            axes: spec.axes.map((a) => ({
              name: a.name,
              hooks: (['up', 'down', 'assert_gone', 'assert_live'] as const).filter((k) => a[k]),
            })),
          });
        }

        // ---- what actually exists on this host --------------------------------------
        if (path === '/api/stacks' && req.method === 'GET') {
          const r = await createRunner({ dryRun: false, level: 'quiet' }).run(
            'docker compose ls --all --format json',
          );
          let projects: unknown = [];
          try {
            projects = JSON.parse(r.stdout || '[]');
          } catch {
            // Older compose emits table output; surface raw rather than pretending.
            return json({ raw: r.stdout, parseError: true, busy: [] });
          }
          const list = (projects as Array<{ Name?: string; Status?: string; ConfigFiles?: string }>)
            .map((p) => ({
              name: p.Name ?? '',
              status: p.Status ?? '',
              busy: jobs.isBusy(p.Name ?? ''),
            }));
          return json({ stacks: list });
        }

        // ---- per-stack --------------------------------------------------------------
        const m = /^\/api\/stacks\/([^/]+)(?:\/(status|up|down|verify))?$/.exec(path);
        if (m) {
          const id = decodeURIComponent(m[1]!);
          const action = m[2];
          const spec = await specFor(id);
          const runner = runnerFor(spec);

          if (action === 'status' || !action) {
            return json({ stack: spec.stack, busy: jobs.isBusy(spec.stack), status: await status(spec, runner) });
          }

          if (req.method !== 'POST') return json({ error: 'use POST' }, { status: 405 });

          const body = (await req.json().catch(() => ({}))) as { verify?: boolean };
          const job = jobs.start(spec.stack, action as 'up' | 'down' | 'verify', (sink) => {
            if (action === 'up') return up(spec, runner, sink);
            if (action === 'verify') return verify(spec, runner, sink);
            return down(spec, runner, { verify: body.verify ?? true }, sink);
          });
          if (!job) {
            return json(
              { error: `stack ${spec.stack} already has a job in flight`, stack: spec.stack },
              { status: 409 },
            );
          }
          return json({ job: { id: job.id, stack: job.stack, action: job.action, state: job.state } }, { status: 202 });
        }

        // ---- jobs -------------------------------------------------------------------
        if (path === '/api/jobs') return json({ jobs: jobs.list() });

        const jm = /^\/api\/jobs\/([^/]+)(?:\/(stream))?$/.exec(path);
        if (jm) {
          const job = jobs.get(decodeURIComponent(jm[1]!));
          if (!job) return json({ error: 'no such job' }, { status: 404 });
          if (jm[2] !== 'stream') return json({ job });

          // SSE: replay the buffered log, then stream live until the job ends.
          const stream = new ReadableStream({
            start(controller) {
              const enc = new TextEncoder();
              const send = (data: unknown) => controller.enqueue(enc.encode(`data: ${JSON.stringify(data)}\n\n`));
              for (const e of job.log) send(e);
              if (job.state !== 'running') {
                send({ done: true, state: job.state });
                controller.close();
                return;
              }
              const off = jobs.subscribe(job.id, (e) => {
                if (e.message === '__end__') {
                  send({ done: true, state: job.state });
                  off();
                  controller.close();
                } else send(e);
              });
            },
          });
          return new Response(stream, {
            headers: {
              'content-type': 'text/event-stream',
              'cache-control': 'no-cache',
              connection: 'keep-alive',
            },
          });
        }

        return json({ error: 'not found' }, { status: 404 });
      } catch (err) {
        if (err instanceof SpecError) return json({ error: `spec: ${err.message}` }, { status: 400 });
        return json({ error: (err as Error).message }, { status: 500 });
      }
    },
  });
}
