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
 *   GET    /api/specs                     named specs (store once, reference from many deployments)
 *   GET    /api/specs/:name               meta always; `source` only with a token (hooks carry secrets)
 *   GET    /api/deployments/:id/source    the stored spec + compose, token required (same reason)
 *   GET    /api/deployments/:id/runtime   containers, networks, ports, the routes their labels declare
 *   GET    /api/registries                private-registry credentials: hosts + usernames, NEVER secrets
 *   PUT    /api/registries/:host           store one  { username, password }  (write-only)
 *   DELETE /api/registries/:host           forget one
 *   GET    /api/routing/live               every route Traefik has, from container labels
 *   GET    /api/routing                   Traefik dynamic config: the file list + whether it is writable
 *   GET    /api/routing/:name             one file's contents, token required
 *   PUT    /api/routing/:name             validate + atomically replace  { content }
 *   DELETE /api/routing/:name             remove it
 *   PUT    /api/specs/:name               store/replace  { spec, compose?, description? }
 *   DELETE /api/specs/:name               refused while a deployment still references it
 *   GET    /api/control                   READ-ONLY view of the control stack (never actionable)
 *   GET    /api/deployments/:id/logs      recent compose logs for a deployment (redacted)
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
// Embedded at BUILD time. The published package is a single bundled file, so reading `ui/` from a
// path relative to the source would point at something that does not ship. It is one small file —
// inlining it also removes a whole class of "works from the repo, 404s once installed" bug.
import UI_HTML_ASSET from '../ui/index.html' with { type: 'text' };

// `@types/bun` types every `.html` import as HTMLBundle (for Bun.serve's route bundling), even with
// `type: 'text'`, but the text loader really does hand back a string — verified by bundling and
// running with the source file deleted. Cast once, here, rather than at the use site.
const UI_HTML = UI_HTML_ASSET as unknown as string;
import { composeLogs, shq } from './compose.ts';
import { createRunner } from './exec.ts';
import { JobRegistry } from './jobs.ts';
import { Registry, RegistryError } from './registry.ts';
import { SpecStore, SpecStoreError } from './specs.ts';
import { RoutingError, RoutingStore } from './routing.ts';
import { allTraefikRouters, deploymentRuntime, detectChallenge } from './inspect.ts';
import { RegistryAuthError, RegistryAuthStore } from './registries.ts';
import { CONTROL_PROJECT } from './init.ts';
import { displayDeclared, redactText } from './redact.ts';
import { parseSpec, SpecError, type Stack } from './spec.ts';
import { down, up, verify } from './stack.ts';

export type ServerOptions = {
  /** Registry root; `new Registry(dataDir)` keeps deployments under `<dataDir>/deployments`. */
  dataDir: string;
  port: number;
  host: string;
  token?: string;
  /**
   * Traefik's dynamic-config directory. Defaults to the in-container mount path
   * (`/etc/traefik/dynamic`); on a host-side run point it at `<dataDir>/control/traefik-dynamic`.
   */
  routingDir?: string;
  /** DOCKER_CONFIG directory holding private-registry credentials. Defaults to `/docker-config`. */
  registryDir?: string;
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
  const specs = new SpecStore(opts.dataDir);
  /**
   * Traefik's watched directory, at the path it has INSIDE this container — the control stack mounts
   * `./traefik-dynamic` to the same place for both services. Overridable for tests and for a CLI run
   * on the host, where the path is `<dataDir>/control/traefik-dynamic` instead.
   */
  const routing = new RoutingStore(opts.routingDir ?? '/etc/traefik/dynamic');
  /**
   * DOCKER_CONFIG for the docker client this process shells out to. Defaults to the in-container mount
   * path; `DOCKER_CONFIG` itself is honoured so the CLI and this module can never disagree about which
   * file a credential lands in.
   */
  const registries = new RegistryAuthStore(
    opts.registryDir ?? process.env.DOCKER_CONFIG ?? '/docker-config',
  );
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

      // ---- the UI ---------------------------------------------------------------------
      // A single embedded document, so there is no filesystem lookup and therefore no path
      // traversal to contain. Every non-/api path serves it: the UI is a one-page app, and a
      // deep link like /deployments/foo should render rather than 404.
      if (!path.startsWith('/api/')) {
        return new Response(UI_HTML, {
          headers: { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' },
        });
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
              | { spec?: unknown; specName?: unknown; vars?: unknown; compose?: unknown; env?: unknown }
              | null;
            if (!body) return json({ error: 'body must be JSON' }, { status: 400 });

            // Two shapes. `specName` points at a stored spec — the common case once you have more
            // than one PR — and `spec` carries an inline copy for a genuine one-off.
            const hasInline = typeof body.spec === 'string' && body.spec.trim() !== '';
            const hasRef = typeof body.specName === 'string' && body.specName.trim() !== '';
            if (hasInline && hasRef) {
              return json(
                { error: 'pass `spec` OR `specName`, not both — one deployment has one source of truth' },
                { status: 400 },
              );
            }
            if (!hasInline && !hasRef) {
              return json(
                {
                  error:
                    'body must be { specName: string, vars?: object } to reference a stored spec, ' +
                    'or { spec: string, compose?: string, env?: object } for an inline one',
                },
                { status: 400 },
              );
            }

            // `vars` are STORED with the deployment, unlike `env` which only validates an inline
            // submission. That is the point: a later `down` then resolves the same stack `up`
            // created without the caller having to remember the variables.
            const vars = coerceEnv(body.vars);
            if (!vars) return json({ error: '`vars` must be a mapping of NAME to a string value' }, { status: 400 });

            let specSource: string;
            let composeSource: string | undefined;
            let specName: string | undefined;
            if (hasRef) {
              specName = (body.specName as string).trim();
              let stored;
              try {
                stored = await specs.get(specName);
              } catch (err) {
                if (err instanceof SpecStoreError) return json({ error: err.message }, { status: 400 });
                throw err;
              }
              if (!stored) return json({ error: `no such spec: ${specName}` }, { status: 404 });
              specSource = await specs.source(specName);
              // Copy the spec's compose file alongside, so the deployment directory is
              // self-contained — compose runs with the deployment dir as cwd.
              const c = Bun.file(`${stored.dir}/compose.yml`);
              if (await c.exists()) composeSource = await c.text();
              // Name every variable the spec needs but was not given, instead of failing later
              // with a parse error that names only the first one.
              const missing = stored.requiredVars.filter(
                (v) => vars[v] === undefined && process.env[v] === undefined,
              );
              if (missing.length > 0) {
                return json(
                  {
                    error: `spec "${specName}" needs variable(s) not supplied: ${missing.join(', ')}`,
                    requiredVars: stored.requiredVars,
                  },
                  { status: 400 },
                );
              }
            } else {
              specSource = body.spec as string;
              if (body.compose !== undefined && typeof body.compose !== 'string') {
                return json({ error: '`compose` must be a string: the compose file contents' }, { status: 400 });
              }
              composeSource = typeof body.compose === 'string' ? body.compose : undefined;
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
              parsed = parseSpec(specSource, { ...process.env, ...vars, ...env });
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
            const dep = await registry.put(id, specSource, { composeYaml: composeSource, env, vars, specName });
            // `vars` ARE stored (unlike `env`, which only validated this submission), so up/down
            // need no query params — and a later `down` cannot target a different stack by
            // forgetting one.
            return json(
              { id: dep.id, kind: dep.kind, stack: parsed.stack, specName: dep.specName ?? null,
                vars: dep.vars ?? {}, createdAt: dep.createdAt, updatedAt: dep.updatedAt },
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
                  ? {
                      file: spec.compose.file,
                      profiles: spec.compose.profiles,
                      overlays: spec.compose.overlays ?? [],
                      // Hostnames and the rule that matches them — no credential, and the pattern is
                      // the thing an operator actually needs when a subdomain is not resolving.
                      subdomains: spec.compose.subdomains ?? [],
                    }
                  : null,
                // Requirement NAMES and hints. A hint is authored guidance ("run `pstack init`
                // first"), not a credential; the `assert` command stays server-side for the same
                // reason axis hook bodies do.
                requires: spec.requires.map((r) => ({ name: r.name, hint: r.hint ?? null })),
                axes: spec.axes.map((a) => ({
                  name: a.name,
                  hooks: (['up', 'down', 'assert_gone', 'assert_live'] as const).filter((k) => a[k]),
                  // Surfaced so the UI can flag the one axis shape `verify` cannot prove clean.
                  verifiable: Boolean(a.assert_gone),
                })),
                // The variables the SPEC declared, values redacted by name. NEVER `spec.env` — that
                // is seeded from the whole ambient environment and holds PSTACK_TOKEN. See the
                // Stack.env doc comment and src/redact.ts.
                env: displayDeclared(spec.declaredEnv, spec.env),
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

        // ---- named specs ----------------------------------------------------------------
        // Store a spec once and point many deployments at it. Before this every deployment carried
        // its own byte-identical copy, so fixing a teardown hook meant re-submitting it per PR.
        if (path === '/api/specs' && req.method === 'GET') {
          return json({ specs: await specs.list() });
        }

        const sm = /^\/api\/specs\/([^/]+)$/.exec(path);
        if (sm) {
          const name = decodeURIComponent(sm[1]!);

          if (req.method === 'PUT') {
            const body = (await req.json().catch(() => null)) as
              | { spec?: unknown; compose?: unknown; description?: unknown }
              | null;
            if (!body || typeof body.spec !== 'string' || !body.spec.trim()) {
              return json(
                { error: 'body must be { spec: string, compose?: string, description?: string }' },
                { status: 400 },
              );
            }
            if (body.compose !== undefined && typeof body.compose !== 'string') {
              return json({ error: '`compose` must be a string: the compose file contents' }, { status: 400 });
            }
            const existed = (await specs.get(name)) !== null;
            const stored = await specs.put(name, body.spec, {
              composeYaml: typeof body.compose === 'string' ? body.compose : undefined,
              description: typeof body.description === 'string' ? body.description : undefined,
            });
            return json({ spec: { ...stored, dir: undefined, specPath: undefined } }, { status: existed ? 200 : 201 });
          }

          const stored = await specs.get(name);
          if (!stored) return json({ error: `no such spec: ${name}` }, { status: 404 });

          if (req.method === 'GET') {
            // THE SOURCE IS A SECRET, THE METADATA IS NOT.
            //
            // Reads are otherwise unauthenticated on this API by design — an operator should be able
            // to see what is running before pasting a token. But a spec's source is the one read
            // that cannot follow that rule: hook bodies are shell strings, and a hook routinely
            // carries a registry password or an API token inline. The resolved-spec view already
            // withholds hook bodies for exactly this reason; serving the whole file unauthenticated
            // handed them out anyway, one route over.
            //
            // So: metadata always (name, kind, description, the NAMES of required variables — none
            // of which is a credential), and the source only with a token. It is withheld
            // EXPLICITLY rather than sent empty, so a page can say why instead of rendering a blank
            // editor that looks like an empty spec.
            const mayReadSource = authed(req);
            return json({
              ...stored,
              dir: undefined,
              specPath: undefined,
              source: mayReadSource ? await specs.source(name) : undefined,
              sourceWithheld: mayReadSource ? undefined : true,
            });
          }

          if (req.method === 'DELETE') {
            // Fail closed. A deployment referencing a deleted spec could never be resolved, and a
            // deployment that cannot be resolved can never be torn down — its containers would
            // outlive every record of how to remove them.
            const users = (await registry.list()).filter((d) => d.specName === name);
            if (users.length > 0) {
              return json(
                {
                  error:
                    `spec "${name}" is referenced by ${users.length} deployment(s) — deleting it ` +
                    `would leave them unresolvable, and an unresolvable deployment can never be ` +
                    `torn down. Remove them first.`,
                  deployments: users.map((d) => d.id),
                },
                { status: 409 },
              );
            }
            await specs.remove(name);
            return json({ deleted: name });
          }

          return json({ error: 'use GET, PUT or DELETE' }, { status: 405 });
        }

        // ---- the control stack: READ ONLY ------------------------------------------------
        // Deliberately has no up/down/verify. The API runs INSIDE this stack, so acting on it would
        // kill the process doing the work (control-plane.md §2) — but being unable to SEE it is just
        // a blind spot, and an operator debugging "why is my preview unreachable" needs to know
        // whether Traefik is even up. Read is safe; write is the invariant.
        if (path === '/api/control' && req.method === 'GET') {
          const ps = await host.run(
            `docker compose -p ${shq(CONTROL_PROJECT)} ps --format json`,
          );
          let services: Array<{ name: string; state: string; health: string; image: string }> = [];
          let parseError = false;
          if (ps.ok) {
            try {
              // `--format json` emits either an array or newline-delimited objects depending on the
              // compose version. Handle both rather than trusting one.
              const raw = ps.stdout.trim();
              const rows = raw.startsWith('[')
                ? (JSON.parse(raw) as unknown[])
                : raw.split('\n').filter(Boolean).map((l) => JSON.parse(l) as unknown);
              services = (rows as Array<Record<string, string>>).map((r) => ({
                name: r.Service ?? r.Name ?? '?',
                state: r.State ?? '?',
                health: r.Health ?? '',
                image: r.Image ?? '',
              }));
            } catch {
              parseError = true;
            }
          }
          return json({
            project: CONTROL_PROJECT,
            // `null` means "docker did not answer", which is NOT the same as "nothing is running".
            reachable: ps.ok,
            parseError,
            services,
            // Stated in the payload so a UI cannot imply an action that must never exist here.
            managedBy: 'pstack init, from the host',
            actionable: false,
            note:
              'The control stack is not managed through this API: the process serving this request ' +
              'runs inside it. Upgrade it with pstack init on the host.',
          });
        }

        // ---- recent logs for a deployment -----------------------------------------------
        const lm = /^\/api\/deployments\/([^/]+)\/logs$/.exec(path);
        if (lm && req.method === 'GET') {
          const id = decodeURIComponent(lm[1]!);
          const dep = await registry.get(id);
          if (!dep) return json({ error: `no such deployment: ${id}` }, { status: 404 });
          const spec = await registry.resolve(id, vars);
          // Bounded: an unbounded tail on a chatty stack would stream megabytes into a browser tab.
          const tailRaw = Number(url.searchParams.get('tail') ?? 200);
          const tail = Number.isFinite(tailRaw) ? Math.min(Math.max(tailRaw, 1), 2000) : 200;
          const r = await composeLogs(spec, runnerFor(spec, dep.dir), tail);
          // Application logs are the one place a secret shows up as free text — an app echoing its
          // own config, a hook printing a connection string. Redact by content before it leaves the
          // host, and mask this process's own token explicitly since it is the worst thing to leak.
          const text = redactText(r.stdout + (r.stderr ? `\n${r.stderr}` : ''), [opts.token ?? '']);
          return json({ stack: spec.stack, tail, ok: r.ok, text });
        }

        // ---- a deployment's stored source ----------------------------------------------
        // Restricted exactly like a named spec's source: hook bodies are shell strings that routinely
        // carry a credential inline, so metadata is public and the file is not. Withheld EXPLICITLY,
        // because a form pre-filled with an empty string is indistinguishable from an empty spec —
        // and this response exists to stop someone re-typing a spec from memory.
        const srcm = /^\/api\/deployments\/([^/]+)\/source$/.exec(path);
        if (srcm && req.method === 'GET') {
          const id = decodeURIComponent(srcm[1]!);
          const dep = await registry.get(id);
          if (!dep) return json({ error: `no such deployment: ${id}` }, { status: 404 });
          if (!authed(req)) return json({ id, specName: dep.specName ?? null, sourceWithheld: true });
          const src = await registry.source(id);
          return json({
            id,
            // Named-spec references keep their own copy of the source, so editing it here DIVERGES
            // from the stored spec every other deployment still shares. Surfaced so a UI can say so
            // rather than letting the fork happen silently.
            specName: dep.specName ?? null,
            spec: src.spec,
            compose: src.compose,
          });
        }

        // ---- what is actually running, and what Traefik was told about it ---------------
        // The registry knows what was submitted and `compose ps` knows what is running; the reason a
        // hostname 404s is in neither — it is in the container's Traefik LABELS, which pstack never
        // writes. Reading them here is what turns "the URL does not work" into a named finding.
        const rtm = /^\/api\/deployments\/([^/]+)\/runtime$/.exec(path);
        if (rtm && req.method === 'GET') {
          const id = decodeURIComponent(rtm[1]!);
          const dep = await registry.get(id);
          if (!dep) return json({ error: `no such deployment: ${id}` }, { status: 404 });
          const spec = await registry.resolve(id, vars);
          // Gathered once and passed in: the router-name collision check is global across the daemon
          // (Traefik's router namespace is), so it cannot be answered from this stack alone.
          const all = await allTraefikRouters(host);
          return json({
            id,
            ...(await deploymentRuntime({
              stack: spec.stack,
              runner: host,
              challenge: await detectChallenge(host),
              allRouters: all.byName,
            })),
          });
        }

        // ---- every route Traefik has, from container labels -----------------------------
        // The file provider (below) is only half of Traefik's configuration and not the half that
        // carries per-PR routes. A page called "routing" that showed only files was answering a
        // question nobody asked.
        if (path === '/api/routing/live' && req.method === 'GET') {
          const all = await allTraefikRouters(host);
          return json({ reachable: all.reachable, routes: all.routes });
        }

        // ---- Traefik dynamic configuration --------------------------------------------
        // The file provider watches a directory, so this is a directory of files rather than one
        // file: middleware, TLS options, the fallback router, routes to things outside compose.
        // Nothing here can lock you out — control.<domain> and api.<domain> are docker labels on
        // this container, not file-provider config (see init.ts).
        if (path === '/api/routing' && req.method === 'GET') {
          return json({
            dir: routing.dir,
            // False on a control stack that predates the mount; the UI turns this into "re-run
            // `pstack init`" rather than letting every write fail one at a time.
            writable: await routing.writable(),
            files: await routing.list(),
          });
        }

        const rm2 = /^\/api\/routing\/([^/]+)$/.exec(path);
        if (rm2) {
          const name = decodeURIComponent(rm2[1]!);

          if (req.method === 'GET') {
            // Authenticated: dynamic config holds basicAuth hashes, forwardAuth URLs and custom
            // headers — the same secret class as a spec's hooks.
            if (!authed(req)) return json({ name, sourceWithheld: true });
            return json({ name, content: await routing.read(name) });
          }

          if (req.method === 'PUT') {
            const body = (await req.json().catch(() => null)) as { content?: unknown } | null;
            if (!body || typeof body.content !== 'string') {
              return json({ error: 'body must be { content: string }' }, { status: 400 });
            }
            // `previous` is the undo: there is deliberately no on-disk history, because the only
            // obvious place to keep it is the one directory that must contain nothing but config.
            const previous = await routing.write(name, body.content);
            return json({ name, previous }, { status: previous === null ? 201 : 200 });
          }

          if (req.method === 'DELETE') {
            return json({ deleted: name, previous: await routing.remove(name) });
          }

          return json({ error: 'use GET, PUT or DELETE' }, { status: 405 });
        }

        // ---- private registry credentials ----------------------------------------------
        // An image pull is authenticated by the CLIENT: `docker pull` reads its own config.json and
        // hands the credential to the daemon. pstack's client runs in this container, so a
        // `docker login` on the host is invisible to it — hence a place to put them here.
        //
        // THERE IS NO READ PATH FOR THE SECRET. `auths` is base64, not encryption, so the response
        // carries hostnames and usernames only. Nothing in registries.ts can return a password.
        if (path === '/api/registries' && req.method === 'GET') {
          return json(await registries.state());
        }

        const regm = /^\/api\/registries\/(.+)$/.exec(path);
        if (regm) {
          const host = decodeURIComponent(regm[1]!);
          if (req.method === 'PUT') {
            const body = (await req.json().catch(() => null)) as
              | { username?: unknown; password?: unknown }
              | null;
            if (!body || typeof body.username !== 'string' || typeof body.password !== 'string') {
              return json({ error: 'body must be { username: string, password: string }' }, { status: 400 });
            }
            const registry = await registries.put(host, body.username, body.password);
            // The normalized key is echoed because it may differ from what was sent — `docker.io`
            // becomes Docker Hub's canonical key, and a credential stored under the friendly name
            // would silently never be used.
            return json({ registry, stored: true });
          }
          if (req.method === 'DELETE') {
            const removed = await registries.remove(host);
            if (!removed) return json({ error: `no credential stored for ${host}` }, { status: 404 });
            return json({ deleted: host });
          }
          return json({ error: 'use PUT or DELETE' }, { status: 405 });
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
        // A rejected routing file, a bad filename, or a directory that is not mounted — all the
        // caller's to fix, and every message names what to do about it.
        if (err instanceof RoutingError) return json({ error: err.message }, { status: 400 });
        // A malformed registry host, a missing username, or a directory that is not mounted.
        if (err instanceof RegistryAuthError) return json({ error: err.message }, { status: 400 });
        // Also the caller's problem: a spec that will not parse, or a name that cannot be a
        // directory. Mapped here as well as in the deployments branch, because PUT /api/specs/:name
        // has no local catch and was answering 500 for a malformed spec.
        if (err instanceof SpecStoreError) return json({ error: err.message }, { status: 400 });
        return json({ error: (err as Error).message }, { status: 500 });
      }
    },
  });
}
