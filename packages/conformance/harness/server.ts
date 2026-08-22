/**
 * Boot a real `pstack serve` and hand back a base URL.
 *
 * The harness picks a free port itself and passes PSTACK_PORT, then polls /api/health — so neither
 * implementation needs a "print the bound port" contract, and the real `serve` path (the loopback
 * interlock, the directory autodetect, the admin bootstrap) is what runs. A port lost to a race
 * between picking and binding shows up as the child dying with EADDRINUSE; that is retried.
 *
 * Everything a test used to pass to createServer() travels as environment: the data dir, the
 * token, the routing/registry dirs, the readiness and SSO timings (the four knobs 0.28.0 added for
 * exactly this), and a PATH with the docker shim first.
 */
import { mkdirSync, rmSync } from 'node:fs';
import { IMPL, cleanEnv, cliArgv } from './impl.ts';
import { startNullServer } from './null-server.ts';

let counter = 0;
export const tmpd = (tag: string): string =>
  `${(process.env.TMPDIR ?? '/tmp').replace(/\/$/, '')}/pstack-conf-${tag}-${process.pid}-${++counter}-${Math.trunc(performance.now() * 1000)}`;

export type BootOptions = {
  tag?: string;
  dataDir?: string;
  /** `null` boots WITHOUT a token — the loopback dev mode where everyone is root. */
  token?: string | null;
  /** Extra environment; a value of `undefined` removes the variable. */
  env?: Record<string, string | undefined>;
  /** A directory to put first on the child's PATH (a docker shim). */
  pathPrefix?: string;
  readiness?: { pollMs?: number; timeoutMs?: number };
  sso?: { stateTtlS?: number; discoveryTtlS?: number };
  routingDir?: string;
  registryDir?: string;
  domain?: string;
  metricsUrl?: string;
  /** Keep the data dir after stop() — for generators that want what the server wrote. */
  keep?: boolean;
};

export type Booted = {
  port: number;
  base: string;
  dataDir: string;
  token: string | null;
  /** The bearer headers most calls want. Empty when booted without a token. */
  H: Record<string, string>;
  stdout: () => string;
  stderr: () => string;
  stop: () => Promise<void>;
};

export const DEFAULT_TOKEN = 'conf-token-0123456789abcdef0123456789abcdef';

async function freePort(): Promise<number> {
  const s = Bun.serve({ port: 0, hostname: '127.0.0.1', fetch: () => new Response('') });
  const port = s.port as number;
  s.stop(true);
  return port;
}

export async function bootServer(o: BootOptions = {}): Promise<Booted> {
  const dataDir = o.dataDir ?? tmpd(o.tag ?? 'srv');
  mkdirSync(dataDir, { recursive: true });
  const token = o.token === undefined ? DEFAULT_TOKEN : o.token;
  const H: Record<string, string> = token ? { authorization: `Bearer ${token}`, 'content-type': 'application/json' } : { 'content-type': 'application/json' };

  if (IMPL === 'null') {
    const port = await freePort();
    const stop = startNullServer(port);
    return {
      port,
      base: `http://127.0.0.1:${port}`,
      dataDir,
      token,
      H,
      stdout: () => '',
      stderr: () => '',
      stop: async () => {
        stop();
        if (!o.keep) rmSync(dataDir, { recursive: true, force: true });
      },
    };
  }

  for (let attempt = 0; attempt < 3; attempt++) {
    const port = await freePort();
    const env: Record<string, string | undefined> = {
      ...cleanEnv(),
      PSTACK_DATA: dataDir,
      PSTACK_HOST: '127.0.0.1',
      PSTACK_PORT: String(port),
      PSTACK_TOKEN: token ?? undefined,
      // Receivers and fake providers live on 127.0.0.1; the SSRF guard must let the test reach them.
      PSTACK_NOTIFY_ALLOW_PRIVATE: '1',
      PSTACK_ROUTING_DIR: o.routingDir,
      DOCKER_CONFIG: o.registryDir,
      PSTACK_DOMAIN: o.domain,
      PSTACK_TRAEFIK_METRICS: o.metricsUrl,
      PSTACK_READINESS_POLL_MS: o.readiness?.pollMs !== undefined ? String(o.readiness.pollMs) : undefined,
      PSTACK_READINESS_TIMEOUT_MS: o.readiness?.timeoutMs !== undefined ? String(o.readiness.timeoutMs) : undefined,
      PSTACK_SSO_STATE_TTL_S: o.sso?.stateTtlS !== undefined ? String(o.sso.stateTtlS) : undefined,
      PSTACK_SSO_DISCOVERY_TTL_S: o.sso?.discoveryTtlS !== undefined ? String(o.sso.discoveryTtlS) : undefined,
      PATH: o.pathPrefix ? `${o.pathPrefix}:${process.env.PATH ?? ''}` : process.env.PATH,
      ...o.env,
    };
    for (const k of Object.keys(env)) if (env[k] === undefined) delete env[k];

    const proc = Bun.spawn(cliArgv(['serve']), { env: env as Record<string, string>, stdout: 'pipe', stderr: 'pipe' });
    let out = '';
    let err = '';
    const drain = async (s: ReadableStream<Uint8Array>, into: (t: string) => void) => {
      const dec = new TextDecoder();
      for await (const chunk of s) into(dec.decode(chunk, { stream: true }));
    };
    void drain(proc.stdout as ReadableStream<Uint8Array>, (t) => (out += t));
    void drain(proc.stderr as ReadableStream<Uint8Array>, (t) => (err += t));

    const base = `http://127.0.0.1:${port}`;
    const deadline = Date.now() + 10_000;
    let exited: number | null = null;
    void proc.exited.then((c) => (exited = c));
    while (Date.now() < deadline) {
      if (exited !== null) break;
      try {
        const r = await fetch(`${base}/api/health`, { signal: AbortSignal.timeout(500) });
        if (r.ok) {
          return {
            port,
            base,
            dataDir,
            token,
            H,
            stdout: () => out,
            stderr: () => err,
            stop: async () => {
              proc.kill('SIGTERM');
              const gone = await Promise.race([proc.exited, Bun.sleep(2000).then(() => null)]);
              if (gone === null) proc.kill('SIGKILL');
              await proc.exited;
              if (!o.keep) rmSync(dataDir, { recursive: true, force: true });
            },
          };
        }
      } catch {
        /* not up yet */
      }
      await Bun.sleep(10);
    }
    if (exited !== null && /EADDRINUSE|address already in use/i.test(err)) continue;
    proc.kill('SIGKILL');
    throw new Error(`pstack serve did not come up on ${base} (exit ${exited ?? 'still running'}).\n--- stdout ---\n${out}\n--- stderr ---\n${err}`);
  }
  throw new Error('could not find a free port in three attempts');
}

/** Poll until `pred` holds or `ms` elapse. Returns the last value. */
export async function until<T>(fn: () => Promise<T>, pred: (v: T) => boolean, ms = 5000, every = 10): Promise<T> {
  const deadline = Date.now() + ms;
  let last = await fn();
  while (!pred(last) && Date.now() < deadline) {
    await Bun.sleep(every);
    last = await fn();
  }
  return last;
}

/** Wait for a job to leave `running`. */
export async function waitJob(s: Booted, id: string, ms = 10_000): Promise<{ state: string; outcome?: unknown; error?: string; log?: unknown[] }> {
  const job = await until(
    async () => ((await (await fetch(`${s.base}/api/jobs/${id}`, { headers: s.H })).json()) as { job?: { state: string } }).job,
    (j) => !!j && j.state !== 'running',
    ms,
  );
  if (!job || job.state === 'running') throw new Error(`job ${id} never finished`);
  return job as { state: string; outcome?: unknown; error?: string; log?: unknown[] };
}
