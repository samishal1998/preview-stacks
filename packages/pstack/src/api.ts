/**
 * HTTP API + static UI host — the control plane's remote surface.
 *
 *   GET    /api/health                    liveness, auth mode, data dir, version   (no auth)
 *   POST   /api/auth/login                { username, password } → session cookie  (no auth)
 *   POST   /api/auth/logout               clears the session                       (no auth)
 *   POST   /api/auth/bootstrap            first admin, PSTACK_TOKEN bearer, only while none exist
 *   GET    /api/auth/me                   who am I
 *   GET|POST /api/users, DELETE /api/users/:id
 *   PUT    /api/users/:id/password       { password } — also revokes that user's sessions+tokens
 *   GET|POST /api/tokens, DELETE /api/tokens/:id   personal API tokens, scoped to the caller
 *   GET    /api/host-vars                variables (with values) + secrets (names only)
 *   PUT    /api/host-vars/:name          { value, secret } — a secret's value is write-only
 *   DELETE /api/host-vars/:name
 *
 *   EVERYTHING BELOW REQUIRES AUTH — session cookie, personal token, or PSTACK_TOKEN. Reads
 *   included: job outcomes carry captured credentials by design (docs/secret-exposure.md), so an
 *   unauthenticated read was a credential feed.
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
 *   GET    /api/deployments/:id/logs?service=&tail=&timestamps=1&since=10m   compose logs
 *   GET    /api/deployments/:id/logs/stream   the same, FOLLOWED — SSE, one line per frame
 *   GET    /api/deployments/:id/runtime   containers, networks, ports, the routes their labels declare
 *   GET    /api/deployments/:id/readiness  is it SERVING yet — poll, or `?wait=<seconds>` to long-poll
 *   POST   /api/deployments/:id/containers/:name/(start|stop|restart)   one container, not the stack
 *   GET    /api/notifiers                 registrations — metadata only, NEVER the signing secret
 *   POST   /api/notifiers                 register  { name, events[], config{}, type? } → 201 { notifier, secret }
 *   PATCH  /api/notifiers/:id             { enabled }
 *   DELETE /api/notifiers/:id             forget it (deliveries cascade)
 *   POST   /api/notifiers/:id/test        send a synthetic delivery now
 *   GET    /api/notifiers/:id/deliveries  recent attempts (+ how many are queued)
 *   POST   /api/notifiers/:id/deliveries/:deliveryId/redeliver   send that event again
 *   GET    /api/notifiers/meta            event names + per-type form fields (drives the UI)
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
 *   POST   /api/jobs/:jobId/cancel        stop a running job — undoes NOTHING
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
import { composeLogs, composeLogsCommand, shq } from './compose.ts';
import { createRunner } from './exec.ts';
import { JobRegistry } from './jobs.ts';
import { Registry, RegistryError } from './registry.ts';
import { SpecStore, SpecStoreError } from './specs.ts';
import { RoutingError, RoutingStore } from './routing.ts';
import { allTraefikRouters, deploymentRuntime, detectChallenge } from './inspect.ts';
import { RegistryAuthError, RegistryAuthStore } from './registries.ts';
import { Store } from './store.ts';
import { Auth, AuthError, type Principal } from './auth.ts';
import { Webhooks, WebhookError } from './webhooks.ts';
import { Dispatcher, NotifierError, TYPES, validateConfig } from './notify.ts';
import { EVENTS, WILDCARD, events, type EventName } from './events.ts';
import { CONTROL_PROJECT } from './init.ts';
import { displayDeclared, redactText } from './redact.ts';
import { parseSpec, SpecError, type Stack } from './spec.ts';
import { down, up, verify } from './stack.ts';
import { publicConfig, redactForNotifier, typeOf } from './notify.ts';
import { HostVars, HostVarsError } from './hostvars.ts';
import { ReadinessWatcher } from './readiness.ts';
import type { Sink } from './log.ts';
import {
  actorOf,
  execArgv,
  isShell,
  mayOpenTerminal,
  SHELLS,
  TerminalAudit,
  type Shell,
  type TerminalData,
} from './terminal.ts';

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
  /**
   * The argv that opens a container shell. Defaults to `docker exec -i <id> <shell>`.
   *
   * Injectable for the same reason `Runner` is: the machine this is developed on has no Docker, so
   * the socket plumbing is proven end to end against a real local `sh` instead of asserted about a
   * command nobody ran. Not a production knob.
   */
  terminalArgv?: (containerId: string, shell: Shell) => string[];
  /**
   * Readiness watch tuning: how often the watcher re-reads docker, and how long it waits before
   * calling a stack timed out. Injectable so a test can converge in milliseconds instead of minutes;
   * the defaults (2s / 180s) are what a host runs.
   */
  readiness?: { pollMs?: number; timeoutMs?: number };
};

const json = (body: unknown, init: ResponseInit = {}) =>
  new Response(JSON.stringify(body, null, 2), {
    ...init,
    headers: { 'content-type': 'application/json', ...(init.headers ?? {}) },
  });

/**
 * CR → NL for terminal input. See the `message` handler: with no pty there is no line discipline, so
 * this is the only thing standing between "Enter" and a shell that never runs anything.
 *
 * CRLF collapses to a single NL rather than becoming two, or every Enter from a client that sends
 * both would run the line and then an empty one.
 */
export function crToNl(message: string | Buffer): string | Uint8Array {
  if (typeof message === 'string') return message.replace(/\r\n?/g, '\n');
  const src = new Uint8Array(message);
  const out = new Uint8Array(src.length);
  let n = 0;
  for (let i = 0; i < src.length; i++) {
    if (src[i] === 0x0d) {
      out[n++] = 0x0a;
      if (src[i + 1] === 0x0a) i++; // CRLF → one NL
    } else {
      out[n++] = src[i]!;
    }
  }
  return out.subarray(0, n);
}

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
  const store = new Store(opts.dataDir);
  const auth = new Auth(store);
  const hooks = new Webhooks(store, publicConfig);
  const terminals = new TerminalAudit(store);
  const hostVars = new HostVars(store);
  /** Every registry.resolve on this server goes through here, so a spec referencing
   *  ${vars.*}/${secrets.*} resolves identically on every route. */
  const resolveDep = (id: string, vars: Record<string, string | undefined> = {}) =>
    registry.resolve(id, vars, hostVars.resolveMaps());
  /**
   * A sink that scrubs host-secret VALUES from every job log line, at the one choke point all of
   * up/down/verify write through. Hooks echo whatever they like — `echo $DB_PASSWORD` is one
   * debugging session away — and by-name redaction cannot catch a value, only content can.
   */
  const scrubbedSink = (inner: Sink): Sink => {
    const secrets = hostVars.secretValues();
    if (secrets.length === 0) return inner;
    return { emit: (level, message) => inner.emit(level, redactText(message, secrets)) };
  };
  const terminalArgv = opts.terminalArgv ?? execArgv;
  /**
   * Per-server, like the dispatcher and for the same reason: its poll loops are timers, and a server
   * that has been stopped must not keep calling docker every two seconds for the life of the process.
   */
  const readiness = new ReadinessWatcher(opts.readiness ?? {});
  /**
   * Live `compose logs --follow` children, so stopping the server stops them too.
   *
   * Same reasoning as the readiness watcher and the event dispatcher: these outlive the request that
   * created them, and a stopped server that leaves compose processes attached to containers is a
   * leak the operator cannot see and did not cause.
   */
  const followers = new Set<() => void>();
  const dispatcher = new Dispatcher(hooks);
  /**
   * The bus is a module singleton and this listener is per-server. A process can host several servers
   * over a test run (and a long-lived one could be restarted in place), so the subscription must end
   * when the server does — otherwise one event fans out into every database any server ever opened,
   * most of them belonging to something already finished. See the `stop` override below.
   */
  const detachDispatcher = events.on((e) => dispatcher.dispatch(e));
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
  const runnerFor = (spec: Stack, dir: string, signal?: AbortSignal) =>
    createRunner({
      dryRun: false,
      level: 'quiet',
      cwd: dir,
      baseEnv: { ...process.env, ...spec.env } as Record<string, string>,
      // Present only for a job's own runner: it is the handle `POST /api/jobs/:id/cancel` pulls, and
      // without it a cancel would flip the record's state while the shell command kept running.
      signal,
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

  /**
   * Who this request is. Three ways in, checked cheapest-first:
   *
   *   Bearer PSTACK_TOKEN   → root (the machine credential; predates accounts, CI holds it)
   *   Bearer pstack_pat_…   → the token's user
   *   Cookie pstack_session → the session's user — the only form a browser can attach to
   *                           EventSource and WebSocket, which is why sessions are cookies at all
   *
   * With no PSTACK_TOKEN configured, everything is root: that is the loopback-only dev mode, and
   * `serve` refuses to bind off-loopback in it.
   */
  const principal = (req: Request): Principal | null => {
    if (!opts.token) return { kind: 'root' };
    const h = req.headers.get('authorization') ?? '';
    const m = /^Bearer (.+)$/.exec(h);
    if (m) {
      const bearer = m[1]!;
      // Constant-time-ish: compare lengths first, then content. Not a hardened comparison — the
      // threat model is "don't leave it wide open", not resisting a timing oracle.
      if (bearer.length === opts.token.length && bearer === opts.token) return { kind: 'root' };
      if (bearer.startsWith('pstack_pat_')) {
        const user = auth.tokenUser(bearer);
        if (user) return { kind: 'user', user };
      }
      /*
       * A bearer that does not authenticate FALLS THROUGH to the cookie below — this used to
       * `return null`, and that turned a stray header into a permanent lockout.
       *
       * What actually happened: Settings holds the token in a `type="password"` input, browsers and
       * password managers autofill those regardless of `autocomplete="off"`, and the UI persists
       * whatever lands there and attaches it to every request. One autofill and a perfectly good
       * session 401'd on every route, with no way out but clearing site data. The input is hardened
       * too, but THIS is the half that makes the whole class non-fatal: whatever else a request
       * carries, a live session is still a live session.
       *
       * It grants nothing — falling through can only authenticate the caller as the session they
       * already hold. A request with no cookie (CI, a script, a wrong PAT) still gets its 401 from
       * the loop below finding nothing.
       */
    }
    for (const candidate of sessionCandidates(req)) {
      const user = auth.sessionUser(candidate);
      if (user) return { kind: 'user', user };
    }
    return null;
  };

  /**
   * EVERY `pstack_session` value in the Cookie header, not just the first.
   *
   * A browser can legitimately hold two cookies with the same name — one set with `Secure` over
   * https and one over plain http, or a survivor from a previous server database — and it sends
   * BOTH. Reading only the first meant that when the stale one sorted ahead (per RFC 6265, older
   * and longer-path cookies do), the live session behind it was never consulted: login "succeeded",
   * every request 401'd, and the only escape was clearing cookies by hand. A dead candidate costs
   * one hash lookup; a locked-out operator costs a support thread.
   */
  const sessionCandidates = (req: Request): string[] => {
    const header = req.headers.get('cookie') ?? '';
    const out: string[] = [];
    for (const m of header.matchAll(/(?:^|;\s*)pstack_session=([^;]+)/g)) {
      if (m[1]) out.push(m[1]);
    }
    return out;
  };

  const authed = (req: Request): boolean => principal(req) !== null;

  /**
   * The session cookie. `Secure` only when the request arrived over TLS (Traefik sets
   * X-Forwarded-Proto) — hardcoding it would break plain-HTTP loopback development, and omitting it
   * would let a production cookie travel over HTTP.
   */
  const sessionCookie = (req: Request, value: string, maxAgeSeconds: number): string => {
    const secure = req.headers.get('x-forwarded-proto') === 'https' ? '; Secure' : '';
    return `pstack_session=${value}; HttpOnly; Path=/; SameSite=Lax; Max-Age=${maxAgeSeconds}${secure}`;
  };

  // Typed so `ws.data` IS a TerminalData rather than a cast at three call sites.
  const server = Bun.serve<TerminalData>({
    port: opts.port,
    hostname: opts.host,
    idleTimeout: 240, // SSE streams must outlive the default (measured: it does NOT apply to
    //                   upgraded WebSockets, so an idle terminal needs no keepalive)
    /**
     * The container terminal. The upgrade decision — auth, which deployment, which container, is it
     * running — is made in `fetch` above, because once the socket is up the only way to say "no" is
     * a close frame the client has to interpret.
     */
    websocket: {
      open(ws) {
        const d = ws.data;
        let proc: ReturnType<typeof Bun.spawn>;
        try {
          proc = Bun.spawn(d.argv, { stdin: 'pipe', stdout: 'pipe', stderr: 'pipe' });
        } catch (err) {
          ws.send(`\r\n[pstack] could not start a shell: ${(err as Error).message}\r\n`);
          ws.close(1011, 'spawn failed');
          return;
        }
        d.proc = {
          kill: () => proc.kill(),
          stdinWrite: (chunk) => {
            const w = proc.stdin as { write: (c: unknown) => void; flush?: () => void };
            w.write(chunk);
            w.flush?.();
          },
        };
        ws.send(`[pstack] ${d.shell} in ${d.containerName} — no TTY: no prompt, no job control, no curses UIs.\r\n`);

        // Binary frames: terminal output is bytes, and decoding to a string here would corrupt a
        // multi-byte character split across two reads. xterm.js takes a Uint8Array directly.
        const pump = async (stream: ReadableStream<Uint8Array>) => {
          try {
            for await (const chunk of stream) {
              if (ws.readyState !== 1) return;
              ws.send(chunk);
            }
          } catch {
            // The process died or the socket went away mid-read; `exited` below reports it once.
          }
        };
        void pump(proc.stdout as ReadableStream<Uint8Array>);
        void pump(proc.stderr as ReadableStream<Uint8Array>);
        void proc.exited.then((code) => {
          if (ws.readyState === 1) {
            ws.send(`\r\n[pstack] shell exited (${code}).\r\n`);
            ws.close(1000, 'shell exited');
          }
        });
      },
      message(ws, message) {
        const d = ws.data;
        // Nothing is interpreted EXCEPT the newline, and that one is not optional.
        //
        // A terminal emulator sends CR (\r) for Enter — correct, and what a real terminal's line
        // discipline then converts to NL before the shell sees it. There is no line discipline here
        // (that is what the missing pty WAS), so a shell reading a pipe never sees a complete line:
        // every keystroke arrives, nothing ever runs, and the terminal looks dead while being
        // perfectly connected. Standing in for that one conversion is the whole fix.
        //
        // Beyond that, keystrokes go to the shell verbatim. There is no command allowlist because
        // there could not be a meaningful one — this IS a shell, and WHICH CONTAINER it runs in was
        // decided at upgrade time. That is where the boundary is.
        try {
          d.proc?.stdinWrite(crToNl(message));
        } catch {
          /* the shell exited between the keystroke and here */
        }
      },
      close(ws) {
        const d = ws.data;
        try {
          d.proc?.kill();
        } catch {
          /* already gone */
        }
        terminals.close(d.sessionId);
      },
    },
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
        return json({
          ok: true,
          authEnforced: !!opts.token,
          // Whether accounts exist — the login page needs this BEFORE authenticating, to say
          // "sign in" versus "no accounts yet, bootstrap one" instead of a dead login form.
          hasUsers: auth.userCount() > 0,
          dataDir: opts.dataDir,
          version: pkg.version,
        });
      }

      // ---- authentication -------------------------------------------------------------
      // These run BEFORE the auth gate below, or nobody could ever log in.
      try {
        if (path === '/api/auth/login' && req.method === 'POST') {
          const body = (await req.json().catch(() => null)) as
            | { username?: unknown; password?: unknown }
            | null;
          if (!body || typeof body.username !== 'string' || typeof body.password !== 'string') {
            return json({ error: 'body must be { username, password }' }, { status: 400 });
          }
          const { session, user } = await auth.login(body.username, body.password);
          return json(
            { user },
            { headers: { 'set-cookie': sessionCookie(req, session, 30 * 24 * 60 * 60) } },
          );
        }

        if (path === '/api/auth/logout' && req.method === 'POST') {
          // Every candidate, same reason as principal(): with duplicate cookies, revoking only the
          // first can leave the session the browser will actually use next time still alive.
          for (const candidate of sessionCandidates(req)) auth.logout(candidate);
          // Expire the cookie regardless — logging out must always succeed.
          return json({ ok: true }, { headers: { 'set-cookie': sessionCookie(req, '', 0) } });
        }

        if (path === '/api/auth/bootstrap' && req.method === 'POST') {
          // Gated by PSTACK_TOKEN specifically, not by principal(): its whole purpose is to work
          // before any account exists.
          const h = /^Bearer (.+)$/.exec(req.headers.get('authorization') ?? '')?.[1];
          if (opts.token && h !== opts.token) {
            return json({ error: 'bootstrap requires the PSTACK_TOKEN bearer' }, { status: 401 });
          }
          const body = (await req.json().catch(() => null)) as
            | { username?: unknown; password?: unknown }
            | null;
          if (!body || typeof body.username !== 'string' || typeof body.password !== 'string') {
            return json({ error: 'body must be { username, password }' }, { status: 400 });
          }
          const user = await auth.bootstrap(body.username, body.password);
          // 409, not silent success: "already bootstrapped" and "created" must be distinguishable,
          // or a replayed bootstrap call reads as having set the password it carries.
          if (!user) return json({ error: 'accounts already exist — bootstrap is only for the first one' }, { status: 409 });
          return json({ user }, { status: 201 });
        }
      } catch (err) {
        if (err instanceof AuthError) return json({ error: err.message }, { status: 400 });
        throw err;
      }

      // ---- THE GATE: every route below requires a principal ---------------------------
      // Reads included. This is the deliberate reversal of the original "reads are open" design,
      // and it retires the exposure documented in docs/secret-exposure.md: job outcomes carry
      // captured credentials BY DESIGN (outputs is the inter-axis env channel), so an
      // unauthenticated read was a credential feed. State stays one login away; values stay
      // behind it.
      const who = principal(req);
      if (!who) return json({ error: 'unauthorized' }, { status: 401 });

      const vars = varsFrom(url);

      try {
      // ---- the signed-in principal, for the UI shell ---------------------------------
      if (path === '/api/auth/me' && req.method === 'GET') {
        return json(who.kind === 'root' ? { root: true } : { root: false, user: who.user });
      }

      // ---- users & personal tokens ----------------------------------------------------
      if (path === '/api/users' && req.method === 'GET') return json({ users: auth.listUsers() });
      const pw = /^\/api\/users\/(\d+)\/password$/.exec(path);
      if (pw && req.method === 'PUT') {
        const body = (await req.json().catch(() => null)) as { password?: unknown } | null;
        if (!body || typeof body.password !== 'string') {
          return json({ error: 'body must be { password }' }, { status: 400 });
        }
        // Revokes that user's sessions and tokens — see `setPassword`. Says so, because a caller
        // changing their OWN password is about to be signed out and should not read that as a bug.
        const ok = await auth.setPassword(Number(pw[1]), body.password);
        if (!ok) return json({ error: `no such user: ${pw[1]}` }, { status: 404 });
        return json({ ok: true, revokedSessions: true });
      }
      if (path === '/api/users' && req.method === 'POST') {
        const body = (await req.json().catch(() => null)) as
          | { username?: unknown; password?: unknown }
          | null;
        if (!body || typeof body.username !== 'string' || typeof body.password !== 'string') {
          return json({ error: 'body must be { username, password }' }, { status: 400 });
        }
        return json({ user: await auth.createUser(body.username, body.password) }, { status: 201 });
      }
      const um = /^\/api\/users\/(\d+)$/.exec(path);
      if (um && req.method === 'DELETE') {
        return auth.deleteUser(Number(um[1]))
          ? json({ deleted: Number(um[1]) })
          : json({ error: 'no such user' }, { status: 404 });
      }

      // Personal tokens are scoped to the CALLER — root has PSTACK_TOKEN and needs none, and one
      // user must not mint or list another's.
      if (path === '/api/tokens') {
        if (who.kind !== 'user') {
          return json({ error: 'personal tokens belong to an account — sign in as one' }, { status: 400 });
        }
        if (req.method === 'GET') return json({ tokens: auth.listTokens(who.user.id) });
        if (req.method === 'POST') {
          const body = (await req.json().catch(() => null)) as { name?: unknown } | null;
          if (!body || typeof body.name !== 'string') {
            return json({ error: 'body must be { name }' }, { status: 400 });
          }
          const made = auth.createToken(who.user.id, body.name);
          // The one and only time the plaintext leaves the server.
          return json({ id: made.id, token: made.token }, { status: 201 });
        }
      }
      const tm = /^\/api\/tokens\/(\d+)$/.exec(path);
      if (tm && req.method === 'DELETE') {
        if (who.kind !== 'user') return json({ error: 'personal tokens belong to an account' }, { status: 400 });
        return auth.deleteToken(who.user.id, Number(tm[1]))
          ? json({ deleted: Number(tm[1]) })
          : json({ error: 'no such token' }, { status: 404 });
      }

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
              spec = await resolveDep(meta.id, vars);
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
              // The same host values the resolve path uses — a spec referencing ${secrets.*}
              // must validate at submission exactly as it will resolve at deploy time.
              parsed = parseSpec(specSource, { ...process.env, ...vars, ...env }, hostVars.resolveMaps());
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

            /**
             * Does another deployment already resolve to this stack?
             *
             * Two ids sharing one stack means two records driving the SAME compose project: `down` on
             * either stops the other's containers, and `verify` on either reports the other's leaks.
             * The way to get there is duplicating a deployment and not changing the `stack:` line (or
             * the variable it interpolates) — which is exactly what the new "duplicate" flow makes
             * easy, so it is worth naming at the moment it happens.
             *
             * A WARNING, NOT A REFUSAL. It can be deliberate (a second record for a stack managed
             * elsewhere), the check cannot know, and refusing a submission over a guess would be worse
             * than saying so. Only for a NEW deployment: on a replace the id already owns the stack.
             */
            const stackSharedWith: string[] = [];
            if (!existed) {
              for (const other of await registry.list()) {
                if (other.id === id) continue;
                // Resolved with the other deployment's own stored vars, which is what its up/down use.
                const s = await resolveDep(other.id, {}).catch(() => null);
                if (s?.stack === parsed.stack) stackSharedWith.push(other.id);
              }
            }

            const dep = await registry.put(id, specSource, {
              composeYaml: composeSource,
              env,
              vars,
              specName,
              host: hostVars.resolveMaps(),
            });
            // Identity only. `specSource`/`composeSource` are in scope right here and both routinely
            // carry inline credentials (a hook is a shell string; a shared deployment's compose holds
            // POSTGRES_PASSWORD), and a webhook URL is outside the auth gate that protects every
            // other reader of them.
            events.emit(existed ? 'deployment.updated' : 'deployment.created', {
              id: dep.id,
              kind: dep.kind,
              stack: parsed.stack,
              specName: dep.specName ?? null,
              // The collision the duplicate flow can create: two records driving one compose project.
              stackSharedWith,
            });
            // `vars` ARE stored (unlike `env`, which only validated this submission), so up/down
            // need no query params — and a later `down` cannot target a different stack by
            // forgetting one.
            return json(
              { id: dep.id, kind: dep.kind, stack: parsed.stack, specName: dep.specName ?? null,
                vars: dep.vars ?? {}, createdAt: dep.createdAt, updatedAt: dep.updatedAt,
                // Omitted when empty, so a client cannot mistake `[]` for "not checked".
                ...(stackSharedWith.length ? { stackSharedWith } : {}) },
              { status: existed ? 200 : 201 },
            );
          }

          const dep = await registry.get(id);
          if (!dep) return json({ error: `no such deployment: ${id}` }, { status: 404 });

          if (!action) {
            if (req.method === 'GET') {
              const spec = await resolveDep(id, vars);
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
              const spec = await resolveDep(id, vars);
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
              events.emit('deployment.deleted', { id, stack: spec.stack, kind: dep.kind });
              return json({ removed: id, stack: spec.stack });
            }

            return json({ error: 'use GET, PUT or DELETE' }, { status: 405 });
          }

          // ---- lifecycle actions ----------------------------------------------------
          if (req.method !== 'POST') return json({ error: 'use POST' }, { status: 405 });

          const body = (await req.json().catch(() => ({}))) as { verify?: boolean; force?: boolean };
          const spec = await resolveDep(id, vars);

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

          const hostSecretValues = hostVars.secretValues();
          const job = jobs.start(
            spec.stack,
            action,
            async (rawSink, signal) => {
              const sink = scrubbedSink(rawSink);
              const runner = runnerFor(spec, dep.dir, signal);
              if (action === 'up') {
                const outcome = await up(spec, runner, sink);
                // Readiness picks up exactly where the job stops. `compose up -d` returns once the
                // containers are CREATED, so the job's success says nothing about whether the app
                // booted — the watch is that second half, and it starts here so a deploy made from
                // CI is watched without the client having to ask. Only after a success: watching a
                // stack whose provisioning failed would report a truthful "not ready" about
                // something nobody deployed.
                // NOT the job's runner: that one dies with the job's signal, and a watch is supposed
                // to outlive the deploy that started it.
                if (outcome.ok) readiness.start(spec.stack, runnerFor(spec, dep.dir), { restart: true });
                return outcome;
              }
              if (action === 'verify') return verify(spec, runner, sink);
              // Teardown makes every pending readiness question moot, and a watch left running would
              // resolve `failed` on the containers it is in the middle of removing.
              readiness.cancel(spec.stack);
              return down(spec, runner, { verify: body.verify ?? true, force: body.force ?? false }, sink);
            },
            // The outcome path: a FAILING hook's stderr lands in step messages, not the sink.
            (text) => (hostSecretValues.length ? redactText(text, hostSecretValues) : text),
          );
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

        // ---- host variables & secrets ----------------------------------------------------
        // The GitHub model, scoped to one host: a VARIABLE is readable configuration, a SECRET's
        // value goes in and never comes back out. Specs reference them as ${vars.NAME} and
        // ${secrets.NAME} — see spec.ts for why the namespace is explicit.
        if (path === '/api/host-vars' && req.method === 'GET') {
          return json({ entries: hostVars.list() });
        }
        const hv = /^\/api\/host-vars\/([A-Za-z0-9_]+)$/.exec(path);
        if (hv) {
          const name = hv[1]!;
          if (req.method === 'PUT') {
            const body = (await req.json().catch(() => null)) as
              | { value?: unknown; secret?: unknown }
              | null;
            if (!body || typeof body.value !== 'string' || typeof body.secret !== 'boolean') {
              return json({ error: 'body must be { value: string, secret: boolean }' }, { status: 400 });
            }
            const r = hostVars.put(name, body.value, body.secret);
            // The value is echoed back ONLY for a variable — a secret's storage is the last time
            // the server ever emits it.
            return json(
              { name, secret: body.secret, value: body.secret ? null : body.value },
              { status: r.created ? 201 : 200 },
            );
          }
          if (req.method === 'DELETE') {
            if (!hostVars.remove(name)) return json({ error: `no such entry: ${name}` }, { status: 404 });
            return json({ deleted: name });
          }
          return json({ error: 'use PUT or DELETE' }, { status: 405 });
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
            events.emit('spec.stored', {
              name: stored.name,
              kind: stored.kind,
              replaced: existed,
              requiredVars: stored.requiredVars,
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
            events.emit('spec.deleted', { name });
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
          const spec = await resolveDep(id, vars);
          // Bounded: an unbounded tail on a chatty stack would stream megabytes into a browser tab.
          const tailRaw = Number(url.searchParams.get('tail') ?? 200);
          const tail = Number.isFinite(tailRaw) ? Math.min(Math.max(tailRaw, 1), 2000) : 200;
          // A compose service name, restricted to what compose itself allows. Validated rather than
          // trusted: it reaches a shell, and `shq` is the second line of defence, not the first.
          const svcRaw = url.searchParams.get('service');
          if (svcRaw !== null && !/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/.test(svcRaw)) {
            return json({ error: `"${svcRaw}" is not a valid compose service name` }, { status: 400 });
          }
          /**
           * A duration compose understands, or nothing.
           *
           * Validated against a narrow shape rather than passed through: it reaches a shell (shq is
           * the second line of defence, not the first), and an unparseable value makes compose fail
           * the whole read — a 400 naming the parameter beats "compose exited non-zero".
           * `10m` / `2h` / `1h30m`, or an RFC3339 instant.
           */
          const duration = (v: string | null): string | undefined => {
            if (!v) return undefined;
            return /^(\d+[smhd])+$/.test(v) || /^\d{4}-\d{2}-\d{2}T[\d:.+Z-]{4,}$/.test(v)
              ? v
              : undefined;
          };
          for (const key of ['since', 'until'] as const) {
            const raw = url.searchParams.get(key);
            if (raw !== null && duration(raw) === undefined) {
              return json(
                { error: `${key}="${raw}" is not a duration (10m, 2h, 1h30m) or an RFC3339 time` },
                { status: 400 },
              );
            }
          }
          const r = await composeLogs(spec, runnerFor(spec, dep.dir), tail, svcRaw ?? undefined, {
            // Opt-in, because it doubles the width of every line and the pretty view lifts it into
            // its own gutter only when asked.
            timestamps: url.searchParams.get('timestamps') === '1',
            since: duration(url.searchParams.get('since')),
            until: duration(url.searchParams.get('until')),
          });
          // Application logs are the one place a secret shows up as free text — an app echoing its
          // own config, a hook printing a connection string. Redact by content before it leaves the
          // host, and mask this process's own token explicitly since it is the worst thing to leak.
          const text = redactText(r.stdout + (r.stderr ? `\n${r.stderr}` : ''), [
            opts.token ?? '',
            // Containers print their own environment freely; a host secret handed to a service via
            // the spec's env block is one crash-handler away from its logs.
            ...hostVars.secretValues(),
          ]);
          // `service` echoed so a UI can tell a stale response from a current one after a switch.
          return json({
            stack: spec.stack,
            tail,
            service: svcRaw ?? null,
            timestamps: url.searchParams.get('timestamps') === '1',
            since: duration(url.searchParams.get('since')) ?? null,
            until: duration(url.searchParams.get('until')) ?? null,
            // How many lines actually came back, so "tail 2000" and "there are only 12 lines" are
            // distinguishable on the page — the difference between a quiet service and a truncated read.
            lines: text ? text.split('\n').length : 0,
            ok: r.ok,
            text,
          });
        }

        /**
         * FOLLOW the logs — SSE, one `docker compose logs --follow` per connection.
         *
         * A stream rather than the poll the fetched read deliberately is NOT a reversal of that
         * rule: polling re-runs a full `--tail 200` every few seconds and re-sends everything each
         * time, while one followed process sends each line once and costs nothing between lines.
         * The rule was "do not hammer the host", and this is the version that does not.
         *
         * The child process is the thing to get right. It is killed when the client disconnects
         * (`cancel`), when the server stops, and after a hard cap — a forgotten tab must not leave a
         * compose process attached to a container for a week. Without the cancel hook every reload
         * would leak one.
         */
        const lstream = /^\/api\/deployments\/([^/]+)\/logs\/stream$/.exec(path);
        if (lstream && req.method === 'GET') {
          const id = decodeURIComponent(lstream[1]!);
          const dep = await registry.get(id);
          if (!dep) return json({ error: `no such deployment: ${id}` }, { status: 404 });
          const spec = await resolveDep(id, vars);
          const svc = url.searchParams.get('service');
          if (svc !== null && !/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/.test(svc)) {
            return json({ error: `"${svc}" is not a valid compose service name` }, { status: 400 });
          }
          const tailRaw = Number(url.searchParams.get('tail') ?? 200);
          const built = await composeLogsCommand(
            spec,
            runnerFor(spec, dep.dir),
            Number.isFinite(tailRaw) ? Math.min(Math.max(tailRaw, 1), 2000) : 200,
            svc ?? undefined,
            { follow: true, timestamps: url.searchParams.get('timestamps') === '1' },
          );
          if (!built) return json({ error: 'this spec has no compose section' }, { status: 400 });

          const secrets = [opts.token ?? '', ...hostVars.secretValues()];
          const proc = Bun.spawn(['bash', '-c', built.cmd], {
            cwd: dep.dir,
            env: { ...process.env, ...spec.env, ...built.env } as Record<string, string>,
            stdout: 'pipe',
            stderr: 'pipe',
          });

          let closed = false;
          let keepalive: ReturnType<typeof setInterval> | null = null;
          let capTimer: ReturnType<typeof setTimeout> | null = null;
          const stop = () => {
            if (closed) return;
            closed = true;
            if (keepalive) clearInterval(keepalive);
            if (capTimer) clearTimeout(capTimer);
            try {
              proc.kill('SIGTERM');
            } catch {
              /* already gone */
            }
          };

          const stream = new ReadableStream({
            start(controller) {
              const enc = new TextEncoder();
              const send = (data: unknown) => {
                if (closed) return;
                try {
                  controller.enqueue(enc.encode(`data: ${JSON.stringify(data)}\n\n`));
                } catch {
                  stop();
                }
              };
              const finish = (reason: string) => {
                if (!closed) send({ done: true, reason });
                stop();
                try {
                  controller.close();
                } catch {
                  /* already closed by cancel */
                }
              };

              // Comment frames, not events: they keep the connection off Bun's idle timeout on a
              // silent stack without a consumer having to filter heartbeat "lines" out of the log.
              keepalive = setInterval(() => {
                if (closed) return;
                try {
                  controller.enqueue(enc.encode(': keepalive\n\n'));
                } catch {
                  stop();
                }
              }, 20_000);
              // The forgotten-tab cap. Generous, because watching a deploy legitimately takes a
              // while; finite, because a compose process per abandoned tab is a real leak.
              capTimer = setTimeout(() => finish('reached the one-hour follow limit'), 3_600_000);

              /** Split a byte stream on newlines, keeping the partial last line for the next chunk. */
              const pump = async (readable: ReadableStream<Uint8Array>, level: string) => {
                const decoder = new TextDecoder();
                let buffer = '';
                for await (const chunk of readable) {
                  if (closed) return;
                  buffer += decoder.decode(chunk, { stream: true });
                  const lines = buffer.split('\n');
                  buffer = lines.pop() ?? '';
                  for (const line of lines) {
                    // Redacted PER LINE, before it leaves the host — the same contract the fetched
                    // read has. A followed log is exactly as likely to print a connection string.
                    send({ level, line: redactText(line, secrets) });
                  }
                }
                if (buffer && !closed) send({ level, line: redactText(buffer, secrets) });
              };

              void Promise.all([
                pump(proc.stdout as ReadableStream<Uint8Array>, 'info'),
                pump(proc.stderr as ReadableStream<Uint8Array>, 'error'),
              ]).catch(() => {});
              // compose exiting means the stack went away (or the project never existed): report it
              // rather than leaving a live-looking stream that will never produce another line.
              void proc.exited.then((code) =>
                finish(code === 0 ? 'compose stopped following' : `compose exited (${code})`),
              );
            },
            cancel() {
              stop();
            },
          });
          followers.add(stop);
          void proc.exited.then(() => followers.delete(stop));
          return new Response(stream, {
            headers: {
              'content-type': 'text/event-stream',
              'cache-control': 'no-cache',
              connection: 'keep-alive',
            },
          });
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

        /**
         * Stop / start / restart ONE container.
         *
         * THE SAME BOUNDARY AS THE TERMINAL, and for a bigger reason. `docker stop` accepts any name
         * on the daemon: `traefik` takes down every preview on the host at once, `pstack-control`
         * takes down the thing being asked. So the name is matched against the containers this
         * deployment actually owns and anything else is a 404 — never trusted from the request.
         *
         * Synchronous, unlike the lifecycle actions. A job exists because `up` takes minutes and
         * holds a per-stack lock; one `docker restart` takes seconds, and putting it behind the lock
         * would make it collide with a deploy for no benefit. It is also NOT compose: `compose
         * restart` acts on a whole service, and the point here is one container out of a replica set.
         */
        const cact = /^\/api\/deployments\/([^/]+)\/containers\/([^/]+)\/(start|stop|restart)$/.exec(path);
        if (cact) {
          if (req.method !== 'POST') return json({ error: 'use POST' }, { status: 405 });
          const id = decodeURIComponent(cact[1]!);
          const wanted = decodeURIComponent(cact[2]!);
          const action = cact[3] as 'start' | 'stop' | 'restart';
          const dep = await registry.get(id);
          if (!dep) return json({ error: `no such deployment: ${id}` }, { status: 404 });

          const spec = await resolveDep(id, vars);
          const rt = await deploymentRuntime({ stack: spec.stack, runner: host, challenge: 'unknown' });
          // "Docker did not answer" is not "you do not own that container" — refusing with a 404
          // there would send someone hunting for a container that is sitting right in front of them.
          if (!rt.reachable) {
            return json(
              { error: 'docker did not answer, so ownership of this container could not be checked' },
              { status: 503 },
            );
          }
          const c = rt.containers.find((x) => x.id === wanted || x.name === wanted);
          if (!c) {
            return json(
              {
                error: `no container "${wanted}" in deployment ${id}`,
                containers: rt.containers.map((x) => x.name),
              },
              { status: 404 },
            );
          }

          // Seconds docker waits for the process to exit before SIGKILL. Clamped: an unbounded value
          // is a request that never returns, and 0 is a hard kill nobody asked for by default.
          const graceRaw = Number(url.searchParams.get('grace') ?? 10);
          const grace = Number.isFinite(graceRaw) ? Math.min(Math.max(Math.trunc(graceRaw), 1), 120) : 10;
          const timing = action === 'start' ? '' : ` -t ${grace}`;
          const r = await host.run(`docker ${action}${timing} ${shq(c.name)}`, {
            label: `docker ${action} ${c.name}`,
          });
          const by = who ? actorOf(who) : 'an operator';

          if (!r.ok) {
            // Docker's own message, not a paraphrase — "container already started" and "no such
            // container" are different problems and it words both better than this layer could.
            return json(
              {
                error: `docker ${action} failed: ${(r.stderr || r.stdout).trim().split('\n')[0] ?? `exit ${r.code}`}`,
                container: c.name,
                action,
              },
              { status: 409 },
            );
          }

          events.emit(`container.${action === 'restart' ? 'restarted' : action === 'stop' ? 'stopped' : 'started'}`, {
            stack: spec.stack,
            deployment: id,
            container: c.name,
            service: c.service,
            action,
            by,
          });

          /*
           * Readiness follows the intent.
           *
           * A start or a restart raises exactly the question a watch answers — does it come back
           * healthy — so one is (re)started and it narrates. A STOP is deliberate, and a watch left
           * running would report `stack.failed` about a container someone meant to stop: a false
           * alarm delivered to every notifier. So stopping cancels it, the same way `down` does.
           */
          if (action === 'stop') readiness.cancel(spec.stack);
          else readiness.start(spec.stack, runnerFor(spec, dep.dir), { restart: true });

          return json({
            container: c.name,
            service: c.service,
            action,
            by,
            // What to expect next, rather than a bare `{ok:true}`: a restarted container is not
            // serving the instant docker returns, and the readiness watch is where that is answered.
            note:
              action === 'stop'
                ? 'Stopped. It stays stopped until something starts it — a deploy, or Start here.'
                : 'Docker has started it. Whether it comes back healthy is what readiness reports.',
          });
        }

        // ---- a shell in a container -----------------------------------------------------
        // THE CONTAINER NAME IS NOT TRUSTED. `docker exec` would accept Traefik, another PR's stack,
        // or the pstack control container itself — whose filesystem is pstack.db, i.e. every password
        // hash and every signing secret. So the request is matched against the containers this
        // deployment actually OWNS, and anything else is a 404. See terminal.ts.
        const term = /^\/api\/deployments\/([^/]+)\/terminal$/.exec(path);
        if (term && req.method === 'GET') {
          const id = decodeURIComponent(term[1]!);
          if (!mayOpenTerminal(who)) {
            return json({ error: 'opening a terminal requires an admin' }, { status: 403 });
          }
          const dep = await registry.get(id);
          if (!dep) return json({ error: `no such deployment: ${id}` }, { status: 404 });
          const shellRaw = url.searchParams.get('shell') ?? 'sh';
          if (!isShell(shellRaw)) {
            return json({ error: `shell must be one of: ${SHELLS.join(', ')}` }, { status: 400 });
          }
          const wanted = url.searchParams.get('container');
          if (!wanted) return json({ error: 'container is required' }, { status: 400 });

          const spec = await resolveDep(id, vars);
          // `challenge: 'unknown'` skips a docker call this route has no use for.
          const rt = await deploymentRuntime({ stack: spec.stack, runner: host, challenge: 'unknown' });
          const c = rt.containers.find((x) => x.id === wanted || x.name === wanted);
          if (!c) {
            return json(
              {
                error: `no container "${wanted}" in deployment ${id}`,
                containers: rt.containers.map((x) => x.name),
              },
              { status: 404 },
            );
          }
          if (!c.state.startsWith('running')) {
            return json({ error: `container "${c.name}" is ${c.state}, not running` }, { status: 409 });
          }

          const data: TerminalData = {
            actor: actorOf(who),
            deployment: id,
            containerId: c.id,
            containerName: c.name,
            shell: shellRaw,
            // The row exists BEFORE the socket does: a session that dies on upgrade still happened.
            sessionId: terminals.open({
              actor: actorOf(who),
              deployment: id,
              container: c.name,
              containerId: c.id,
              shell: shellRaw,
            }),
            argv: terminalArgv(c.id, shellRaw),
          };
          // `upgrade` returning true means the response is the handshake — returning anything here
          // would clobber it.
          if (server.upgrade(req, { data })) return undefined;
          terminals.close(data.sessionId);
          return json({ error: 'this endpoint expects a websocket upgrade' }, { status: 426 });
        }

        if (path === '/api/terminal-sessions' && req.method === 'GET') {
          return json({ sessions: terminals.recent() });
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
          const spec = await resolveDep(id, vars);
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

        // ---- did it actually come up? ---------------------------------------------------
        // The job answers "did the commands run"; this answers "is it serving". Two ways to consume
        // it, because two kinds of caller need it: a plain GET for a page that polls, and
        // `?wait=<seconds>` for a CI step that would otherwise sleep-and-hope. Both return the same
        // snapshot, so a client loops on `state === 'watching'` and never has to catch an edge.
        const rdm = /^\/api\/deployments\/([^/]+)\/readiness$/.exec(path);
        if (rdm && req.method === 'GET') {
          const id = decodeURIComponent(rdm[1]!);
          const dep = await registry.get(id);
          if (!dep) return json({ error: `no such deployment: ${id}` }, { status: 404 });
          const spec = await resolveDep(id, vars);

          const num = (key: string, fallback: number, max: number): number => {
            const raw = Number(url.searchParams.get(key));
            return Number.isFinite(raw) && raw > 0 ? Math.min(raw, max) : fallback;
          };
          // Seconds on the wire (a human writes `?wait=30`), milliseconds inside — named accordingly
          // at the boundary so the two can never be confused.
          const waitMs = num('wait', 0, 60) * 1000;
          const timeoutMs = url.searchParams.has('timeout')
            ? num('timeout', 180, 3600) * 1000
            : undefined;

          // No watch yet — start one, so readiness is answerable for a stack this process did not
          // deploy (a restarted server, or a deploy made from the CLI). `refresh=1` re-asks a
          // question that already has an answer; without it a settled verdict is returned as it
          // stands, `endedAt` and all, rather than being silently recomputed under the caller.
          const existing = readiness.get(spec.stack);
          if (!existing || (existing.state !== 'watching' && url.searchParams.get('refresh') === '1')) {
            // SILENT (`emit: false`): a watch a read started must not announce itself. On a
            // deployment that was never deployed there are no containers to converge, so 180s later
            // this would have posted "did not become ready in time" to every notifier — about a
            // deploy nobody ran, triggered by someone opening a page. The caller still sees every
            // state in the snapshot; only the bus is spared.
            readiness.start(spec.stack, runnerFor(spec, dep.dir), {
              timeoutMs,
              restart: true,
              emit: false,
            });
          }
          const snap = waitMs > 0 ? await readiness.wait(spec.stack, waitMs) : readiness.get(spec.stack);
          return json({ id, ...(snap ?? { stack: spec.stack, state: 'watching', containers: [] }) });
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
            // The name and the action only — the file's CONTENT can hold basicAuth hashes and
            // forwardAuth URLs, and a notifier URL is outside the auth gate that protects it.
            events.emit('routing.changed', { file: name, action: previous === null ? 'created' : 'replaced' });
            return json({ name, previous }, { status: previous === null ? 201 : 200 });
          }

          if (req.method === 'DELETE') {
            const previous = await routing.remove(name);
            events.emit('routing.changed', { file: name, action: 'deleted' });
            return json({ deleted: name, previous });
          }

          return json({ error: 'use GET, PUT or DELETE' }, { status: 405 });
        }

        // ---- notifiers: webhooks now, Slack/Discord/email later --------------------------
        // Inside this try deliberately — a route above it gets no error mapping, which is how the
        // user routes ended up answering 500 with an HTML page for a validation failure.
        //
        // This drives the UI's pickers: the event list and the per-type form fields come from the
        // server, so adding a notifier type does not mean editing the UI.
        //
        // (An earlier note here claimed the order relative to the `:id` route was load-bearing. It
        // is not — that regex captures `(\d+)`, which cannot match `meta` — and a false constraint
        // is worse than none: it makes the next reader afraid to reorganise the block, and teaches
        // them to discount an equivalent claim elsewhere that IS doing real work.)
        if (path === '/api/notifiers/meta' && req.method === 'GET') {
          return json({
            events: EVENTS,
            wildcard: WILDCARD,
            types: Object.values(TYPES).map((ty) => ({
              kind: ty.kind,
              label: ty.label,
              fields: ty.fields,
              // So the UI can say "your receiver needs this secret" only where that is true.
              signs: ty.signs,
            })),
          });
        }

        if (path === '/api/notifiers') {
          if (req.method === 'GET') return json({ notifiers: hooks.list() });
          if (req.method === 'POST') {
            const body = (await req.json().catch(() => null)) as
              | { type?: unknown; name?: unknown; config?: unknown; events?: unknown }
              | null;
            if (!body || typeof body.name !== 'string') {
              return json(
                { error: 'body must be { name, events[], config{}, type? }' },
                { status: 400 },
              );
            }
            const cfg = body.config;
            if (cfg !== undefined && (typeof cfg !== 'object' || cfg === null || Array.isArray(cfg))) {
              return json({ error: '`config` must be an object' }, { status: 400 });
            }
            const kind = typeof body.type === 'string' && body.type ? body.type : 'webhook';
            const made = hooks.create({
              type: kind,
              name: body.name,
              config: (cfg as Record<string, unknown>) ?? {},
              events: Array.isArray(body.events) ? (body.events as string[]) : [],
              validateConfig,
              // `typeOf` throws NotifierError for an unknown kind, which the handler below maps to
              // 400 — the same path an unknown type already took.
              signs: typeOf(kind).signs,
            });
            // The ONLY time the signing secret leaves the server, and `null` for a type that does
            // not sign. There is no read path for it — see webhooks.ts, and the test that greps
            // every response for it.
            return json({ notifier: made.row, secret: made.secret }, { status: 201 });
          }
          return json({ error: 'use GET or POST' }, { status: 405 });
        }

        /**
         * Send a past delivery's event to its notifier AGAIN.
         *
         * The recovery path for a receiver that was down, misconfigured, or deployed broken — before
         * this, the only way to re-fire an event was to re-run the deploy that caused it.
         *
         * WHAT IS REPLAYED is the stored envelope, byte for byte, keeping the original event `id` so
         * a receiver that already processed it still dedupes. Two deliberate differences: the
         * timestamp is re-stamped to now (the signature covers it, and receivers are told to reject
         * stale ones — an hour-old replay would be refused by a correct receiver), and the request
         * carries `x-pstack-redelivery: 1`.
         */
        const rdl = /^\/api\/notifiers\/(\d+)\/deliveries\/(\d+)\/redeliver$/.exec(path);
        if (rdl) {
          if (req.method !== 'POST') return json({ error: 'use POST' }, { status: 405 });
          const nid = Number(rdl[1]);
          const did = Number(rdl[2]);
          const row = hooks.get(nid);
          if (!row) return json({ error: `no such notifier: ${nid}` }, { status: 404 });
          const d = hooks.deliveryWithPayload(did);
          // Belongs-to check, not just existence: `/notifiers/2/deliveries/9/redeliver` must not fire
          // notifier 1's event at notifier 2.
          if (!d || d.notifierId !== nid) {
            return json({ error: `no delivery ${did} for notifier ${nid}` }, { status: 404 });
          }
          if (!d.payload) {
            return json(
              {
                error:
                  `delivery ${did} has no stored payload — it was recorded before payloads were ` +
                  `captured (0.25.0), or it was dropped before an envelope existed. There is nothing ` +
                  `to replay, and inventing one would send an event that never happened.`,
              },
              { status: 409 },
            );
          }
          let stored: { id: string; event: string; data: Record<string, unknown> };
          try {
            stored = JSON.parse(d.payload);
          } catch {
            return json({ error: `delivery ${did} has an unreadable payload` }, { status: 409 });
          }
          dispatcher.redeliver(row, {
            id: stored.id,
            event: stored.event as EventName,
            // NOW, not the original: the signature covers the timestamp and a receiver is told to
            // reject stale ones, so replaying the old stamp would be refused by a correct receiver.
            at: Date.now(),
            data: stored.data ?? {},
          });
          return json({
            redelivered: did,
            notifier: nid,
            event: stored.event,
            eventId: stored.id,
            note:
              'Queued. It carries the original event id, so a receiver that already processed it ' +
              'will dedupe — and x-pstack-redelivery: 1 so one that did not can tell.',
          });
        }

        const nm = /^\/api\/notifiers\/(\d+)(?:\/(test|deliveries))?$/.exec(path);
        if (nm) {
          const nid = Number(nm[1]);
          const sub = nm[2];
          const row = hooks.get(nid);
          if (!row) return json({ error: `no such notifier: ${nid}` }, { status: 404 });

          if (!sub && req.method === 'DELETE') {
            hooks.remove(nid);
            return json({ deleted: nid });
          }
          if (!sub && req.method === 'PATCH') {
            // PATCH, not PUT: the row has a column a caller can never send, so a full-replace verb
            // would be lying about its own semantics.
            const body = (await req.json().catch(() => null)) as { enabled?: unknown } | null;
            if (!body || typeof body.enabled !== 'boolean') {
              return json({ error: 'body must be { enabled: boolean }' }, { status: 400 });
            }
            hooks.setEnabled(nid, body.enabled);
            return json({ notifier: hooks.get(nid) });
          }
          if (sub === 'deliveries' && req.method === 'GET') {
            return json({
              deliveries: hooks
                .deliveries(nid)
                // `replayable` rather than making the UI guess from the age of the row: whether a
                // payload was stored is a fact the server has and the client cannot infer.
                .map((d) => ({ ...d, replayable: hooks.deliveryWithPayload(d.id)?.payload != null })),
              // What is WAITING. Without it a quiet notifier and a backed-up one look identical.
              queued: dispatcher.queued(nid),
            });
          }
          if (sub === 'test' && req.method === 'POST') {
            // Sent DIRECTLY, never through the bus: emitting a real event to test one notifier would
            // notify every other notifier too. There is also no `webhook.*` event name anywhere, so a
            // delivery failure cannot trigger a delivery — that recursion is structurally impossible.
            const ty = Object.hasOwn(TYPES, row.type) ? TYPES[row.type] : undefined;
            if (!ty) return json({ error: `unknown notifier type "${row.type}"` }, { status: 400 });
            const secret = hooks.secretOf(nid) ?? '';
            // RAW, not row.config: the row masks secret-marked fields for display, and a chat
            // type's masked webhookUrl is not a URL — the test would fail with "fetch() URL is
            // invalid" against a perfectly healthy notifier.
            const cfg = hooks.rawConfigOf(nid) ?? row.config;
            const eventId = `evt_test_${Date.now().toString(36)}`;
            // Logged like any other delivery. It updates `lastStatus`, so leaving no row behind would
            // put a status in the list view with nothing in the log to explain it.
            const deliveryId = hooks.startDelivery(nid, eventId, 'test');
            const result = await ty.send(
              {
                id: eventId,
                event: 'job.succeeded',
                at: Date.now(),
                // `test: true` is the contract flag a per-event formatter must check — see
                // `NotifierType.send`. The event name is a real one and would otherwise read as a
                // job that actually succeeded.
                data: { test: true, note: 'Test delivery from pstack — no job ran.' },
              },
              cfg,
              secret,
              AbortSignal.timeout(5_000),
            );
            hooks.finishDelivery(deliveryId, {
              status: result.ok ? 'ok' : 'failed',
              attempts: 1,
              responseCode: result.status,
              error: result.error ? redactForNotifier(result.error, secret, cfg) : undefined,
            });
            hooks.noteResult(nid, result.ok ? 'ok' : 'failed');
            hooks.prune(nid);
            // Redacted on the way OUT too. The dispatcher runs every stored error through the same
            // function; a second send path that skipped it would hand back the very credential the
            // read path above was just changed to mask — for a Slack type, the URL in a fetch error.
            return json({
              result: {
                ...result,
                error: result.error ? redactForNotifier(result.error, secret, cfg) : undefined,
              },
            });
          }
          // PATCH or DELETE on the row; GET or POST only on the sub-routes. The old message said
          // "use GET, PATCH or DELETE", so a GET on :id was told to use the method it had just used.
          return json({ error: 'use PATCH or DELETE here, or /deliveries (GET) and /test (POST)' }, { status: 405 });
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

        /**
         * Stop a running job.
         *
         * NOTHING IS UNDONE. The abort reaches the shell command in flight; every resource created
         * before it stays created, and a half-finished teardown has left whatever it had not reached
         * yet. So the answer says so in the same breath as reporting success — this is the one place
         * a caller learns that "stopped" is not "reverted".
         */
        const cancelm = /^\/api\/jobs\/([^/]+)\/cancel$/.exec(path);
        if (cancelm) {
          if (req.method !== 'POST') return json({ error: 'use POST' }, { status: 405 });
          const jobId = decodeURIComponent(cancelm[1]!);
          const job = jobs.get(jobId);
          if (!job) return json({ error: `no such job: ${jobId}` }, { status: 404 });
          // 409, not 404: the job exists and the caller's request is simply out of date, which is a
          // different thing to fix than a wrong id.
          if (job.state !== 'running') {
            return json(
              { error: `job ${jobId} already finished (${job.state})`, state: job.state },
              { status: 409 },
            );
          }
          // The route is behind the gate, so a principal exists; the fallback is for the type, not
          // for a case that can happen.
          const who = principal(req);
          const by = who ? actorOf(who) : 'an operator';
          jobs.cancel(jobId, by);
          return json({
            cancelled: jobId,
            stack: job.stack,
            action: job.action,
            by,
            warning:
              'Nothing was undone. Whatever this job created or destroyed before it stopped is ' +
              'still that way — run verify to see what exists.',
          });
        }

        const jm = /^\/api\/jobs\/([^/]+)(?:\/(stream))?$/.exec(path);
        if (jm) {
          const job = jobs.get(decodeURIComponent(jm[1]!));
          if (!job) return json({ error: 'no such job' }, { status: 404 });
          if (jm[2] !== 'stream') return json({ job });

          // SSE: replay the buffered log, then stream live until the job ends.
          //
          // THE SUBSCRIPTION MUST OUTLIVE NOTHING. A browser closing the tab, or any client
          // disconnecting mid-job, cancels this stream — and without the `cancel` hook below the
          // subscription stayed registered, so when the job later finished the callback enqueued
          // into a closed controller and threw `Invalid state: Controller is already closed`
          // *inside JobRegistry's `finally`*. Two guards, because either alone leaves a window:
          // unsubscribe on cancel, and treat the controller as one-shot so a late or duplicate
          // event cannot throw.
          let off: (() => void) | null = null;
          let closed = false;
          const stream = new ReadableStream({
            start(controller) {
              const enc = new TextEncoder();
              const send = (data: unknown) => {
                if (closed) return;
                try {
                  controller.enqueue(enc.encode(`data: ${JSON.stringify(data)}\n\n`));
                } catch {
                  // The peer went away between the check and the write. Nothing to report — the
                  // stream is gone either way, and throwing here reaches the job that emitted.
                  closed = true;
                }
              };
              const finish = (state: string) => {
                if (closed) return;
                send({ done: true, state });
                closed = true;
                off?.();
                off = null;
                try {
                  controller.close();
                } catch {
                  /* already closed by cancel */
                }
              };

              for (const e of job.log) send(e);
              if (job.state !== 'running') {
                finish(job.state);
                return;
              }
              off = jobs.subscribe(job.id, (e) => {
                if (e.message === '__end__') finish(job.state);
                else send(e);
              });
            },
            cancel() {
              // The client disconnected. Drop the subscription NOW; otherwise it survives until the
              // job ends and fires into a dead controller.
              closed = true;
              off?.();
              off = null;
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
        if (err instanceof HostVarsError) return json({ error: err.message }, { status: 400 });
        if (err instanceof AuthError) return json({ error: err.message }, { status: 400 });
        // Also the caller's problem: a spec that will not parse, or a name that cannot be a
        // directory. Mapped here as well as in the deployments branch, because PUT /api/specs/:name
        // has no local catch and was answering 500 for a malformed spec.
        if (err instanceof SpecStoreError) return json({ error: err.message }, { status: 400 });
        // A malformed notifier registration, an unknown type, or an undeliverable URL.
        if (err instanceof WebhookError) return json({ error: err.message }, { status: 400 });
        if (err instanceof NotifierError) return json({ error: err.message }, { status: 400 });
        return json({ error: (err as Error).message }, { status: 500 });
      }
    },
  });

  /**
   * Stopping the server must also stop it LISTENING to the bus.
   *
   * `events` is a module singleton; this server's dispatcher is not. Without this, a server that has
   * been stopped keeps receiving every event the process emits and writing deliveries into its own
   * (possibly deleted) database — which in a test suite means one event fanning out into fifteen.
   */
  const stopServing = server.stop.bind(server);
  server.stop = (closeActiveConnections?: boolean) => {
    detachDispatcher();
    for (const stop of followers) stop();
    followers.clear();
    // Same reasoning one level down: a readiness watch is a poll loop, and a stopped server's loop
    // would go on shelling out to docker — and go on emitting events into a bus it no longer serves.
    readiness.stopAll();
    return stopServing(closeActiveConnections);
  };
  return server;
}
