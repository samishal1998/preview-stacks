/**
 * HTTP API + static UI host — the control plane's remote surface.
 *
 *   GET    /api/health                    liveness, auth mode, data dir, version
 *   GET    /api/deployments               every submitted deployment (+ busy, + is it running)
 *   GET    /api/deployments/:id           meta + resolved spec summary
 *   PUT    /api/deployments/:id           submit or replace  { spec, compose?, env? }
 *   DELETE /api/deployments/:id           forget it (refused while containers still exist)
 *   POST   /api/deployments/:id/up        → 202 { job }
 *   POST   /api/deployments/:id/down      → 202 { job }   body: { verify?, force? }
 *   POST   /api/deployments/:id/verify    → 202 { job }
 *   GET    /api/jobs                      recent job transcripts
 *   GET    /api/jobs/:jobId               one job (poll this for state)
 *   GET    /api/jobs/:jobId/stream        SSE live log
 *   GET    /                              the web UI
 *
 * `:id` is a REGISTRY id, not a compose project name. The server owns the stored spec and resolves
 * `stack:` itself, so a client can never ask it to act on an arbitrary compose project on the host.
 *
 * THIS API MUST NEVER MANAGE THE STACK IT RUNS IN. `up` on the deployment containing pstack-api
 * would kill the process performing it, and a failed self-upgrade would leave the host with no
 * control plane and no remote way to fix it. The control stack (traefik + pstack-api + pstack-ui)
 * belongs to `pstack init` / `pstack self-upgrade`, run from the host. Nothing here enforces that —
 * the API cannot reliably know its own deployment id — so don't submit the control stack to it.
 *
 * VARIABLES. A spec interpolates `${VAR}` exactly once, at resolve time. Every route that resolves
 * a deployment merges the request's `?query` parameters (and, for PUT, the body's `env`) OVER
 * `process.env`. So a spec whose stack is `pr-${PR}` must be given it — `GET /api/deployments/42?PR=7`,
 * `POST /api/deployments/42/down?PR=7`. A missing variable surfaces as a 400 naming it, never as a
 * stack called `pr-` that every PR would collide on. Variables are NOT persisted with the
 * deployment: the same ones must accompany `up` and the later `down`, or teardown would target a
 * different stack than deploy created.
 *
 * SECURITY. This API destroys infrastructure. Guards, in order of how much they carry:
 *   1. A bearer token (`PSTACK_TOKEN`), required on every mutating route (POST/PUT/DELETE).
 *   2. With no token set the server binds 127.0.0.1 and refuses 0.0.0.0 (enforced in cli.ts), so an
 *      unauthenticated instance cannot be exposed by accident.
 *   3. Responses never echo the resolved environment. `parseSpec` seeds a spec's variables from the
 *      whole ambient environment, so `Stack.env` holds every secret this process has —
 *      `PSTACK_TOKEN` included. Summaries below are built field-by-field for that reason; never
 *      spread a resolved `Stack` into a response, however convenient it looks while debugging.
 * Query variables land in the environment of the hooks a job runs, which makes them as privileged
 * as the token — that is precisely why the routes that execute anything are the authenticated ones.
 * It is NOT multi-tenant: every caller shares one Docker socket and one registry. Put it behind your
 * ingress' auth (or an SSH tunnel) before letting anyone but you reach it.
 */

import pkg from '../package.json';
import { shq } from './compose.ts';
import { createRunner } from './exec.ts';
import { JobRegistry } from './jobs.ts';
import { Registry, RegistryError } from './registry.ts';
import { parseSpec, SpecError, type Stack } from './spec.ts';
import { down, up, verify } from './stack.ts';

export type ServerOptions = {
  /** Registry root; `new Registry(dataDir)` keeps deployments under `<dataDir>/deployments`. */
  dataDir: string;
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

/** Request variables: `?PR=7&REGION=eu` → `{ PR: '7', REGION: 'eu' }`, layered over process.env. */
const varsFrom = (url: URL): Record<string, string> => Object.fromEntries(url.searchParams);

/**
 * Coerce a PUT body's `env` into string variables. Returns null when the shape is wrong, so the
 * caller can answer 400 rather than interpolating `[object Object]` into a stack name.
 */
function coerceEnv(v: unknown): Record<string, string> | null {
  if (v === undefined || v === null) return {};
  if (typeof v !== 'object' || Array.isArray(v)) return null;
  const out: Record<string, string> = {};
  for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
    if (typeof val === 'string') out[k] = val;
    else if (typeof val === 'number' || typeof val === 'boolean') out[k] = String(val);
    else return null;
  }
  return out;
}

export function createServer(opts: ServerOptions) {
  const jobs = new JobRegistry();
  const registry = new Registry(opts.dataDir);
  /**
   * For host-level queries that belong to no particular deployment (docker inventory).
   *
   * `baseEnv` is not optional here: `Bun.spawn` REPLACES the environment when one is passed, so a
   * runner built without it hands bash an empty env — no PATH, no DOCKER_HOST, no
   * DOCKER_CONTEXT. `docker` then resolves only if it happens to sit in bash's compiled-in default
   * PATH, and a non-default socket is invisible. Since a docker call that cannot run reads as
   * "could not tell", that would make the DELETE guard below refuse forever.
   */
  const host = createRunner({ dryRun: false, level: 'quiet', baseEnv: process.env as Record<string, string> });

  /**
   * A runner scoped to one deployment's directory. `cwd` is load-bearing: a submitted spec says
   * `compose: { file: compose.yml }` and `registry.put` writes that file next to `spec.yml`, so
   * compose must be invoked from the deployment directory. Without it the relative path resolves
   * against the *server's* cwd — either "no such file", or worse, an unrelated compose.yml that
   * happens to sit there.
   *
   * It applies to AXIS HOOKS too — `createRunner` hands this cwd to the one `Bun.spawn` that runs
   * every `bash -c`. Two consequences for a submitted spec:
   *   - hooks must be inline shell or ABSOLUTE paths. `up: ./hooks/db.sh …` cannot work here: only
   *     spec.yml and compose.yml ever live in a deployment directory. (A CLI spec gets away with it
   *     because it runs from your checkout.)
   *   - compose takes its project directory from the first `-f` file, so a relative bind mount
   *     (`./data:/data`) now lands inside the deployment directory — which `registry.remove`
   *     deletes. That is safe only because DELETE refuses while containers exist, which makes the
   *     guard below load-bearing for data, not just for orphan visibility.
   */
  const runnerFor = (spec: Stack, dir: string) =>
    createRunner({
      dryRun: false,
      level: 'quiet',
      cwd: dir,
      baseEnv: { ...process.env, ...spec.env } as Record<string, string>,
    });

  /**
   * Compose project names docker knows about, or null when it could not answer. Best-effort by
   * design — one call for the whole list, and an old compose that cannot emit JSON yields null so
   * the response says "unknown" instead of confidently claiming nothing is running.
   */
  const composeProjects = async (): Promise<Set<string> | null> => {
    const r = await host.run('docker compose ls --all --format json');
    if (!r.ok) return null;
    try {
      const rows = JSON.parse(r.stdout || '[]') as Array<{ Name?: string }>;
      return new Set(rows.map((p) => p.Name ?? '').filter(Boolean));
    } catch {
      return null;
    }
  };

  /**
   * Container ids belonging to a compose project, read straight from the labels compose stamps on
   * them. Used only by DELETE, where a wrong answer orphans containers, so it does not share the
   * best-effort path above: `docker ps` needs no compose file, `-a` counts a stopped-but-present
   * container (exactly the orphan case), and null means "docker could not answer" — never "empty".
   */
  const containersFor = async (stack: string): Promise<string[] | null> => {
    const r = await host.run(`docker ps -aq --filter ${shq(`label=com.docker.compose.project=${stack}`)}`);
    if (!r.ok) return null;
    return r.stdout.split('\n').map((l) => l.trim()).filter(Boolean);
  };

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
        return json({ ok: true, authEnforced: !!opts.token, dataDir: opts.dataDir, version: pkg.version });
      }

      const mutating = req.method === 'POST' || req.method === 'PUT' || req.method === 'DELETE';
      if (mutating && !authed(req)) return json({ error: 'unauthorized' }, { status: 401 });

      const vars = varsFrom(url);

      try {
        // ---- the whole registry -----------------------------------------------------
        if (path === '/api/deployments' && req.method === 'GET') {
          const projects = await composeProjects();
          const deployments = [];
          for (const meta of await registry.list()) {
            // One deployment with a missing variable must not fail the whole listing — that is how
            // an operator loses sight of everything else on the box. Report it per row instead.
            let spec: Stack | null = null;
            let unresolved: string | undefined;
            try {
              spec = await registry.resolve(meta.id, vars);
            } catch (err) {
              unresolved = (err as Error).message;
            }
            deployments.push({
              ...meta,
              stack: spec?.stack ?? null,
              // Both of these are `null`, never `false`, when they could not be determined: an
              // unresolved spec has no stack name to look up, and a docker that did not answer is
              // not the same fact as "nothing is running". Collapsing either to `false` is how a UI
              // reports a live stack as torn down, or an in-flight one as safe to act on.
              busy: spec ? jobs.isBusy(spec.stack) : null,
              running: spec && projects ? projects.has(spec.stack) : null,
              unresolved,
            });
          }
          return json({ deployments });
        }

        // ---- one deployment ---------------------------------------------------------
        const m = /^\/api\/deployments\/([^/]+)(?:\/(up|down|verify))?$/.exec(path);
        if (m) {
          const id = decodeURIComponent(m[1]!);
          const action = m[2] as 'up' | 'down' | 'verify' | undefined;

          // PUT is the one route that does not require the deployment to exist yet.
          if (!action && req.method === 'PUT') {
            const body = (await req.json().catch(() => null)) as
              | { spec?: unknown; compose?: unknown; env?: unknown }
              | null;
            if (!body || typeof body.spec !== 'string' || !body.spec.trim()) {
              return json({ error: 'body must be { spec: string, compose?: string, env?: object }' }, { status: 400 });
            }
            if (body.compose !== undefined && typeof body.compose !== 'string') {
              return json({ error: '`compose` must be a string: the compose file contents' }, { status: 400 });
            }
            const env = coerceEnv(body.env);
            if (!env) return json({ error: '`env` must be a mapping of NAME to a string value' }, { status: 400 });

            // Validate BEFORE anything touches disk. `registry.put` writes spec.yml first and, if
            // the spec then fails to load, `rm -rf`s the deployment directory. On a REPLACE that
            // would delete a perfectly good record over a typo — while its containers keep running,
            // now invisible to the control plane, which is the exact leak this project prevents.
            // `parseSpec` takes the source string and never touches the filesystem, so the same
            // errors are caught here, with the raw SpecError text rather than a wrapped one.
            let parsed: Stack;
            try {
              parsed = parseSpec(body.spec, { ...process.env, ...env });
            } catch (err) {
              if (err instanceof SpecError) return json({ error: `spec: ${err.message}` }, { status: 400 });
              throw err;
            }

            // Swapping the spec mid-job means the eventual `down` tears down with different
            // profiles and axes than `up` created — the same orphan class as deleting the record.
            if (jobs.isBusy(parsed.stack)) {
              return json(
                { error: `stack ${parsed.stack} has a job in flight — wait for it before replacing the spec`, stack: parsed.stack },
                { status: 409 },
              );
            }

            const existed = (await registry.get(id)) !== null;
            const dep = await registry.put(id, body.spec, { composeYaml: body.compose, env });
            // `env` was used to validate, not stored: the registry keeps the spec, not the
            // variables. Whoever calls up/down later must pass the same ones as ?query params.
            return json(
              { id: dep.id, kind: dep.kind, stack: parsed.stack, createdAt: dep.createdAt, updatedAt: dep.updatedAt },
              { status: existed ? 200 : 201 },
            );
          }

          const dep = await registry.get(id);
          if (!dep) return json({ error: `no such deployment: ${id}` }, { status: 404 });

          if (!action) {
            if (req.method === 'GET') {
              const spec = await registry.resolve(id, vars);
              // Field-by-field on purpose — see guard 3 in the header. Axis HOOK NAMES, never hook
              // bodies: a hook is a shell string that routinely carries an API token inline.
              return json({
                id: dep.id,
                kind: dep.kind,
                createdAt: dep.createdAt,
                updatedAt: dep.updatedAt,
                stack: spec.stack,
                busy: jobs.isBusy(spec.stack),
                compose: spec.compose
                  ? { file: spec.compose.file, profiles: spec.compose.profiles, overlays: spec.compose.overlays ?? [] }
                  : null,
                requires: spec.requires.map((r) => r.name),
                axes: spec.axes.map((a) => ({
                  name: a.name,
                  hooks: (['up', 'down', 'assert_gone', 'assert_live'] as const).filter((k) => a[k]),
                })),
              });
            }

            if (req.method === 'DELETE') {
              const spec = await registry.resolve(id, vars);
              if (jobs.isBusy(spec.stack)) {
                return json(
                  { error: `stack ${spec.stack} has a job in flight`, stack: spec.stack },
                  { status: 409 },
                );
              }
              const containers = await containersFor(spec.stack);
              // Fail closed. Forgetting a deployment whose containers still exist orphans them
              // beyond the control plane's view — nothing left knows their stack name, their axes,
              // or how to tear them down, which is precisely the leak this project exists to
              // prevent. "Docker would not answer" is not evidence of absence, so it refuses too.
              if (containers === null) {
                return json(
                  {
                    error:
                      `cannot confirm ${spec.stack} is torn down — docker did not answer. Refusing to ` +
                      `forget a deployment that may still have containers.`,
                    stack: spec.stack,
                  },
                  { status: 409 },
                );
              }
              if (containers.length > 0) {
                return json(
                  {
                    error:
                      `${spec.stack} still has ${containers.length} container(s). Run down first — ` +
                      `removing the record now would orphan them beyond the control plane's view.`,
                    stack: spec.stack,
                    containers: containers.length,
                  },
                  { status: 409 },
                );
              }
              await registry.remove(id);
              return json({ removed: id, stack: spec.stack });
            }

            return json({ error: 'use GET, PUT or DELETE' }, { status: 405 });
          }

          // ---- lifecycle actions ----------------------------------------------------
          if (req.method !== 'POST') return json({ error: 'use POST' }, { status: 405 });

          const body = (await req.json().catch(() => ({}))) as { verify?: boolean; force?: boolean };
          const spec = await registry.resolve(id, vars);
          const runner = runnerFor(spec, dep.dir);

          // Answer the shared-kind refusal synchronously rather than handing back a job id that is
          // going to fail: the caller learns immediately, and the reason is not buried in a
          // transcript. Deliberate duplication — `down()` in stack.ts holds the authoritative
          // guard, so a direct library caller cannot bypass it either, and `force` is passed
          // straight through rather than being consumed here.
          if (action === 'down' && spec.kind === 'shared' && !body.force) {
            return json(
              {
                error:
                  `refusing to tear down "${spec.stack}": kind is \`shared\`. down runs compose down -v, ` +
                  `which destroys volumes every tenant depends on. Re-send with { "force": true } if ` +
                  `that is truly intended.`,
                stack: spec.stack,
                kind: spec.kind,
              },
              { status: 409 },
            );
          }

          const job = jobs.start(spec.stack, action, (sink) => {
            if (action === 'up') return up(spec, runner, sink);
            if (action === 'verify') return verify(spec, runner, sink);
            return down(spec, runner, { verify: body.verify ?? true, force: body.force ?? false }, sink);
          });
          if (!job) {
            // One job per stack: a `down` racing an `up` over the same database branch is
            // corruption, not contention, so the conflict is surfaced instead of queued.
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
        // A missing `${VAR}` and a malformed spec are both the caller's problem, and both name the
        // offending variable/field in their message — 400 with that text beats a 500 with none.
        if (err instanceof SpecError) return json({ error: `spec: ${err.message}` }, { status: 400 });
        if (err instanceof RegistryError) return json({ error: err.message }, { status: 400 });
        return json({ error: (err as Error).message }, { status: 500 });
      }
    },
  });
}
