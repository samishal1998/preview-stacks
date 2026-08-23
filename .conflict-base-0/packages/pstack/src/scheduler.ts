/**
 * The sleep scheduler and wake-on-call.
 *
 * THE PROBLEM. Most previews are deployed to be LOOKED AT ONCE — by a reviewer, by a smoke test — and
 * then sit there until the PR merges, holding memory, CPU and a hostname for days. Tearing them down
 * on a timer loses the state (a seeded database branch, uploaded fixtures) that made them useful;
 * leaving them up is what makes a preview host need a bigger box every quarter.
 *
 * SLEEP is the middle: the compose project goes down (volumes and axes stay — stack.ts `sleep`), and
 * the NEXT REQUEST to its hostname brings it back. The reviewer who opens the link on day three sees
 * "spinning up…" for the length of an `up`, then the app. Nobody has to remember it exists.
 *
 * ── HOW A SLEEPING HOSTNAME FINDS ITS WAY BACK ────────────────────────────────────────────────────
 *
 * Traefik routes a preview hostname to its container while the container runs. When it does not, a
 * catch-all router on the pstack container (priority 1, `HostRegexp` over the whole preview domain —
 * rendered by init.ts) matches instead and the request lands HERE, with the original `Host`. The
 * `SleepIndex` maps that hostname to a deployment: the hostnames were captured from the stack's live
 * Traefik labels the moment before it slept (registry `SleepRecord`), so a hand-written `Host()` rule
 * is recognised exactly as a generated one, and a wildcard `HostRegexp` still matches its subdomains.
 * A match starts a `wake` job (unless one is running) and answers the "spinning up" page, which polls
 * itself until the response stops carrying `x-pstack-wake` — i.e. until Traefik routes the hostname to
 * the app again — and reloads.
 *
 * ── WHAT "IDLE" MEANS, AND WHERE IT COMES FROM ───────────────────────────────────────────────────
 *
 * Traefik already counts every request per router (`traefik_router_requests_total`, Prometheus
 * format, on an entrypoint the control stack exposes to this container only). The meter reads it once
 * per tick and notes, per stack, the last tick on which any of its routers' counters moved. That is
 * the whole activity model: no access-log parsing, no per-request hook, nothing in the request path.
 *
 * Everything the meter knows is IN MEMORY (invariant 10): a restart forgets when a stack was last
 * visited, so the idle clock restarts from the process start — the cost is a stack staying up one
 * more `idle` period, never one going to sleep early. (ponytail: in-memory last-activity; persist a
 * per-stack timestamp if a restart-heavy host makes the extra period matter.)
 *
 * `after` needs no meter: the newest container's start time IS the last deploy, read from docker.
 *
 * ── WHAT THE SCHEDULER WILL NOT DO ───────────────────────────────────────────────────────────────
 *
 *   - Sleep a `kind: shared` deployment (stack.ts refuses too) — every preview depends on it.
 *   - Sleep a stack with a job in flight, or one already asleep, or one it cannot resolve.
 *   - Sleep on `idle` when the meter cannot read Traefik: silence from an unreachable endpoint is not
 *     the same fact as no traffic. It logs that once and keeps evaluating `after`.
 *   - Wake anything from a page VIEW on the control plane: only a request to the preview's own
 *     hostname wakes it. Listing deployments is not using one.
 */

import type { DeploymentMeta } from './registry.ts';
import type { Stack } from './spec.ts';
import type { Runtime } from './inspect.ts';

/** `2h` for 7_200_000 — the reason string a human reads in the event and the job transcript. */
export function formatDuration(ms: number): string {
  const units: Array<[number, string]> = [[86_400_000, 'd'], [3_600_000, 'h'], [60_000, 'm'], [1_000, 's']];
  const parts: string[] = [];
  let rest = ms;
  for (const [size, label] of units) {
    const n = Math.floor(rest / size);
    if (n > 0) {
      parts.push(`${n}${label}`);
      rest -= n * size;
    }
  }
  return parts.join('') || '0s';
}

// ── hostname → sleeping deployment ──────────────────────────────────────────────────────────────

/**
 * Which sleeping deployment a hostname belongs to. Rebuilt from the registry whenever a sleep record
 * changes and on a slow timer (an operator may edit meta.json over SSH); a request costs one Map
 * lookup plus a regexp per wildcard rule, never a disk read.
 */
export class SleepIndex {
  #hosts = new Map<string, string>();
  #rules: Array<{ re: RegExp; id: string }> = [];

  rebuild(deployments: DeploymentMeta[]): void {
    const hosts = new Map<string, string>();
    const rules: Array<{ re: RegExp; id: string }> = [];
    for (const d of deployments) {
      if (!d.sleep) continue;
      for (const h of d.sleep.hosts) hosts.set(h.toLowerCase(), d.id);
      for (const r of d.sleep.rules) {
        try {
          // Go and JS agree on the subset a hostname rule uses (anchors, classes, escapes).
          rules.push({ re: new RegExp(r, 'i'), id: d.id });
        } catch {
          /* an unparseable pattern matches nothing — the exact hosts still do */
        }
      }
    }
    this.#hosts = hosts;
    this.#rules = rules;
  }

  find(host: string): string | null {
    const h = host.toLowerCase();
    const exact = this.#hosts.get(h);
    if (exact) return exact;
    for (const r of this.#rules) if (r.re.test(h)) return r.id;
    return null;
  }

  get size(): number {
    return this.#hosts.size + this.#rules.length;
  }
}

/** Split what `hostsFromRule` returns into exact hostnames and `HostRegexp` patterns. */
export function splitHosts(hosts: string[]): { hosts: string[]; rules: string[] } {
  const out = { hosts: [] as string[], rules: [] as string[] };
  for (const h of hosts) {
    if (h.startsWith('(pattern) ')) out.rules.push(h.slice('(pattern) '.length));
    else out.hosts.push(h);
  }
  return out;
}

// ── traffic ─────────────────────────────────────────────────────────────────────────────────────

/**
 * Reads Traefik's request counters and remembers when each router last moved.
 *
 * `fetchImpl` is injectable so a test can feed it metrics text; the default is the global fetch with
 * a short timeout, because a tick that hangs on Traefik would stall every other decision.
 */
export class TrafficMeter {
  #url: string | undefined;
  #fetch: (url: string) => Promise<string | null>;
  /** router name (without `@provider`) → last total seen. */
  #totals = new Map<string, number>();
  /** router name → when its total last increased. */
  #moved = new Map<string, number>();
  #warned = false;
  /** Whether the most recent read succeeded — `idle` is only decidable when it did. */
  ok = false;
  log: (line: string) => void;

  constructor(url: string | undefined, opts: { fetchImpl?: (url: string) => Promise<string | null>; log?: (line: string) => void } = {}) {
    this.#url = url;
    this.log = opts.log ?? (() => {});
    this.#fetch =
      opts.fetchImpl ??
      (async (u) => {
        try {
          const r = await fetch(u, { signal: AbortSignal.timeout(5_000) });
          return r.ok ? await r.text() : null;
        } catch {
          return null;
        }
      });
  }

  /** One read. Safe to call on every tick; never throws. */
  async sample(now: number = Date.now()): Promise<void> {
    if (!this.#url) {
      this.ok = false;
      return;
    }
    const text = await this.#fetch(this.#url);
    if (text === null) {
      this.ok = false;
      if (!this.#warned) {
        this.#warned = true;
        this.log(`scheduler: cannot read Traefik metrics at ${this.#url} — \`sleep.idle\` will not trigger until it answers (sleep.after still does)`);
      }
      return;
    }
    this.ok = true;
    this.#warned = false;
    const next = new Map<string, number>();
    for (const line of text.split('\n')) {
      if (!line.startsWith('traefik_router_requests_total{')) continue;
      const m = /router="([^"@]+)(?:@[^"]*)?"[^}]*}\s+([0-9.e+]+)/.exec(line);
      if (!m) continue;
      const router = m[1]!;
      next.set(router, (next.get(router) ?? 0) + Number(m[2]));
    }
    for (const [router, total] of next) {
      const prev = this.#totals.get(router);
      // Strictly greater: a counter reset (Traefik restarted) reads lower and is a new baseline, not a visit.
      if (prev !== undefined && total > prev) this.#moved.set(router, now);
    }
    this.#totals = next;
  }

  /** When any of these routers last saw a request, or null if never (since this process started). */
  lastActivity(routers: string[]): number | null {
    let latest: number | null = null;
    for (const r of routers) {
      const t = this.#moved.get(r);
      if (t !== undefined && (latest === null || t > latest)) latest = t;
    }
    return latest;
  }
}

// ── the loop ────────────────────────────────────────────────────────────────────────────────────

export type SchedulerDeps = {
  list(): Promise<DeploymentMeta[]>;
  resolve(id: string): Promise<Stack>;
  runtime(spec: Stack): Promise<Runtime>;
  isBusy(stack: string): boolean;
  /** Start the sleep job. The scheduler never tears anything down itself. */
  sleep(id: string, spec: Stack, reason: string): void;
  meter: TrafficMeter;
  log?: (line: string) => void;
  /** Tick cadence. A minute on a host; milliseconds in a test. */
  tickMs?: number;
  now?: () => number;
};

export class Scheduler {
  #deps: SchedulerDeps;
  #timer: ReturnType<typeof setInterval> | null = null;
  #running = false;
  readonly bootAt: number;

  constructor(deps: SchedulerDeps) {
    this.#deps = deps;
    this.bootAt = (deps.now ?? Date.now)();
  }

  start(): void {
    if (this.#timer) return;
    const ms = this.#deps.tickMs ?? 60_000;
    this.#timer = setInterval(() => void this.tick(), ms);
    // A ticking scheduler must not keep a finished process (a test, a CLI) alive.
    (this.#timer as { unref?: () => void }).unref?.();
  }

  stop(): void {
    if (this.#timer) clearInterval(this.#timer);
    this.#timer = null;
  }

  /** One evaluation of every deployment. Public so a test can drive it without waiting. */
  async tick(): Promise<void> {
    if (this.#running) return; // a slow docker must not stack ticks
    this.#running = true;
    try {
      const now = (this.#deps.now ?? Date.now)();
      await this.#deps.meter.sample(now);
      for (const meta of await this.#deps.list()) {
        if (meta.sleep) continue; // already asleep
        if (meta.kind === 'shared') continue;
        let spec: Stack;
        try {
          spec = await this.#deps.resolve(meta.id);
        } catch {
          continue; // unresolvable — nothing to act on, and the list route already reports it
        }
        const policy = spec.sleep;
        if (!policy || !spec.compose) continue;
        if (this.#deps.isBusy(spec.stack)) continue;

        const rt = await this.#deps.runtime(spec);
        if (!rt.reachable || rt.containers.length === 0) continue; // unknown, or nothing to sleep

        const lastDeploy = rt.containers.reduce<number | null>(
          (acc, c) => (c.startedAt !== null && (acc === null || c.startedAt > acc) ? c.startedAt : acc),
          null,
        );

        if (policy.afterMs !== undefined && lastDeploy !== null && now - lastDeploy >= policy.afterMs) {
          this.#deps.sleep(meta.id, spec, `after ${formatDuration(policy.afterMs)}`);
          continue;
        }

        if (policy.idleMs !== undefined && this.#deps.meter.ok) {
          const routers = rt.routes.map((r) => r.router);
          // The idle clock starts at the later of: boot (nothing earlier is known), the last deploy
          // (a redeploy is a visit), the last request.
          const floor = Math.max(this.bootAt, lastDeploy ?? 0);
          const since = Math.max(floor, this.#deps.meter.lastActivity(routers) ?? 0);
          if (now - since >= policy.idleMs) {
            this.#deps.sleep(meta.id, spec, `idle ${formatDuration(policy.idleMs)}`);
          }
        }
      }
    } catch (err) {
      this.#deps.log?.(`scheduler: tick failed: ${(err as Error).message}`);
    } finally {
      this.#running = false;
    }
  }
}

// ── the page ────────────────────────────────────────────────────────────────────────────────────

const esc = (s: string) => s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);

/**
 * What a visitor sees while the stack wakes. Self-contained (no assets: this is served on the
 * PREVIEW's hostname, where nothing of the control plane exists) and it polls ITSELF: when the
 * response stops carrying `x-pstack-wake`, Traefik has started routing the hostname to the app, and a
 * reload lands on it. `error` is the last wake job's failure, shown so a broken image is not an
 * infinite spinner.
 */
export function wakePage(args: { host: string; stack: string; state: 'waking' | 'busy' | 'failed'; error?: string }): string {
  const title = args.state === 'failed' ? 'Your preview could not start' : 'Your preview is spinning up…';
  const detail =
    args.state === 'failed'
      ? `The last attempt to wake <b>${esc(args.stack)}</b> failed: <code>${esc(args.error ?? 'unknown error')}</code>. Reload to try again.`
      : `<b>${esc(args.stack)}</b> was asleep. It is being brought back now — this page reloads by itself when it answers.`;
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>${esc(title)}</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="15">
<style>
  body{margin:0;min-height:100vh;display:grid;place-items:center;font:16px/1.5 system-ui,sans-serif;background:#0f1115;color:#e6e8ee}
  main{max-width:34rem;padding:2rem;text-align:center}
  h1{font-size:1.4rem;font-weight:600;margin:1rem 0 .5rem}
  p{margin:.25rem 0;color:#aab0bd}
  code{font-family:ui-monospace,monospace;background:#1b1f27;padding:.1rem .35rem;border-radius:4px;color:#e6e8ee}
  .spin{width:2.5rem;height:2.5rem;margin:0 auto;border:3px solid #2a2f3a;border-top-color:#7aa2f7;border-radius:50%;animation:r 1s linear infinite}
  .failed .spin{display:none}
  small{display:block;margin-top:1.5rem;color:#6b7280}
  @keyframes r{to{transform:rotate(360deg)}}
</style></head>
<body class="${args.state}"><main>
  <div class="spin" role="status" aria-label="loading"></div>
  <h1>${esc(title)}</h1>
  <p>${detail}</p>
  <small>${esc(args.host)} · pstack wake-on-call</small>
</main>
<script>
  (function () {
    var tries = 0;
    function poll() {
      fetch(location.href, { cache: 'no-store', redirect: 'manual' }).then(function (r) {
        if (r.headers.get('x-pstack-wake') !== '1' && r.status < 500) { location.reload(); return; }
        tries++; setTimeout(poll, tries < 20 ? 3000 : 8000);
      }).catch(function () { tries++; setTimeout(poll, 5000); });
    }
    setTimeout(poll, 3000);
  })();
</script>
</body></html>
`;
}
