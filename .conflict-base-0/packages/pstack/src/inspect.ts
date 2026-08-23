/**
 * What is actually running, and what Traefik was actually told about it.
 *
 * WHY THIS EXISTS. "The hostname does not work" was undiagnosable from this tool. The registry knows
 * what you *submitted*; `compose ps` knows what is *running*; and the reason a request 404s lives in
 * neither — it is in the container's Traefik **labels**, which pstack never writes and never read.
 * So the answer was always "SSH in and run `docker inspect`".
 *
 * Every check below is one of the five rules in `examples/docker-compose.preview.yml`, turned from
 * prose nobody re-reads into a finding on the page. In particular:
 *
 *   - The control stack runs Traefik with `--providers.docker.exposedbydefault=false`, so a container
 *     **without `traefik.enable=true` is invisible**. No error anywhere; the hostname simply 404s.
 *   - Traefik dials containers on `preview-ingress`. A service not attached to it is up, healthy and
 *     unreachable — and if the compose file declared the network non-external, compose silently made
 *     `<project>_preview-ingress` instead, which looks correct in every listing.
 *   - Traefik's router namespace is **global across the daemon**, so two PRs that both define
 *     `traefik.http.routers.app` overwrite each other and one PR's hostname serves the other's
 *     container. That one cannot be found by looking at a single deployment, so the collision check
 *     reads every container on the host.
 *
 * ── NEVER RETURN A RAW INSPECT ───────────────────────────────────────────────────────────────────
 *
 * `docker inspect` includes `Config.Env` — the container's whole environment, which is where database
 * passwords and API tokens live. Nothing here passes an inspect payload through: each field is picked
 * out by name, and the label values that are kept get `redactText` applied, because a
 * `traefik.http.middlewares.*.basicauth.users` label is a credential too.
 *
 * ── SWARM ────────────────────────────────────────────────────────────────────────────────────────
 *
 * A swarm stack is discovered differently in three ways, all handled here so no caller has to know:
 *   - its containers carry `com.docker.stack.namespace=<stack>`, not the compose project label, and
 *     the service name is `<stack>_<svc>` under `com.docker.swarm.service.name`;
 *   - Traefik's routes come from SERVICE labels (`docker service inspect … .Spec.Labels`), which no
 *     task container carries — so the routes and the findings about them are computed per service;
 *   - a task may run on another node, where this daemon's `docker ps` cannot see it. Those come from
 *     `docker stack ps` and appear as containers with `remote: true` and a `node` — listed honestly,
 *     and refused by the routes that would have to `docker exec` into them.
 * Only tasks whose desired state is `running` count: swarm replaces a failed task rather than
 * restarting the container, so `docker ps -a` on a manager accumulates corpses that would otherwise
 * read as a stack that "failed".
 */

import type { Runner } from './exec.ts';
import { redactText } from './redact.ts';
import { shq } from './compose.ts';
import type { Orchestrator } from './spec.ts';
import { NODE_LABEL, SERVICE_LABEL, STACK_LABEL, TASK_LABEL } from './swarm.ts';

/** One published port mapping, as `docker inspect` reports it. */
export type PortMap = {
  /** Port inside the container, e.g. 80. */
  containerPort: number;
  protocol: string;
  /** Host-side port when one is published. Absent is the normal case here — Traefik dials the
   *  container over the shared network, so a preview service usually publishes nothing. */
  hostPort?: string;
};

export type ContainerInfo = {
  id: string;
  name: string;
  /** The compose service this container implements, from `com.docker.compose.service`. */
  service: string | null;
  image: string;
  state: string;
  /** `healthy` / `unhealthy` / `starting`, or null when the image declares no healthcheck. */
  health: string | null;
  /**
   * Exit status, meaningful only once `state` is `exited`. It is the difference between "the job
   * container finished its work" and "the app crashed on boot", which is the whole question a
   * readiness watch is asking — see `readiness.ts`.
   */
  exitCode: number | null;
  /**
   * How many times Docker has restarted it. With a `restart:` policy a container that dies on boot
   * cycles exited → restarting → running → exited, so no single sample of `state` can tell a crash
   * loop from a slow start; this counter can, because it only goes up.
   */
  restartCount: number;
  networks: string[];
  /**
   * The container's IP on the ingress network, which is the address Traefik actually dials. Null when
   * the container is not on that network — which is precisely the "healthy but unreachable" case.
   */
  ingressIp: string | null;
  ports: PortMap[];
  /** Traefik labels only, values redacted. Never the container's environment. */
  traefikLabels: Record<string, string>;
  /** When the process started (epoch ms); null when docker did not say. The scheduler's "last deploy". */
  startedAt: number | null;
  /** The swarm node it runs on (hostname), or null under compose. */
  node: string | null;
  /**
   * True when this is a swarm task on ANOTHER node: listed from `docker stack ps`, not inspected, and
   * out of reach of `docker exec`/`stop` from here. Every field above is what the manager knows.
   */
  remote: boolean;
};

/** A Traefik router as declared by labels, joined to the service (and port) it points at. */
export type RouteInfo = {
  router: string;
  /** The container that declares it. */
  container: string;
  rule: string;
  /** Hostnames extracted from `Host(...)`, plus the pattern for a `HostRegexp(...)`. */
  hosts: string[];
  service: string | null;
  /** The container port this router's service forwards to, when a label declares one. */
  port: number | null;
  entrypoints: string | null;
  tls: boolean;
  certresolver: string | null;
  priority: string | null;
  /**
   * What Traefik forwards to, as it builds it: the container's IP on the ingress network plus the
   * port from the service label. NOT `service_name:port` — the docker provider resolves a container
   * IP, which is why the ingress attachment and the *container-internal* port are what matter, and
   * why no `ports:` publishing is needed at all.
   */
  target: string | null;
};

export type Finding = {
  level: 'error' | 'warn' | 'info';
  /** Short, specific, and it names the fix. */
  message: string;
};

export type Runtime = {
  stack: string;
  containers: ContainerInfo[];
  routes: RouteInfo[];
  findings: Finding[];
  /** Which ACME challenge the host's Traefik is configured for; decides the certresolver rule. */
  challenge: 'http01' | 'dns01' | 'unknown';
  /** True when docker answered. False means every field above is "unknown", never "empty". */
  reachable: boolean;
};

const INGRESS = 'preview-ingress';

/** `docker inspect` output for the fields this module reads. Everything else is ignored. */
type RawInspect = {
  Id?: string;
  Name?: string;
  RestartCount?: number;
  Args?: string[];
  Config?: { Image?: string; Labels?: Record<string, string>; Cmd?: string[] };
  State?: { Status?: string; ExitCode?: number; StartedAt?: string; Health?: { Status?: string } };
  NetworkSettings?: {
    Networks?: Record<string, { IPAddress?: string } | undefined>;
    Ports?: Record<string, Array<{ HostPort?: string }> | null>;
  };
};

/**
 * Inspect a set of container ids.
 *
 * One `docker inspect` for all of them rather than one each: on a host with a dozen previews the
 * per-call overhead dominates, and this runs on a page load.
 */
async function inspectIds(runner: Runner, ids: string[]): Promise<RawInspect[]> {
  if (ids.length === 0) return [];
  const r = await runner.run(`docker inspect ${ids.map(shq).join(' ')}`, { label: 'docker inspect' });
  if (!r.ok) return [];
  try {
    const parsed = JSON.parse(r.stdout || '[]');
    return Array.isArray(parsed) ? (parsed as RawInspect[]) : [];
  } catch {
    return [];
  }
}

/** Container ids carrying a label, e.g. a compose project. `-a` so a stopped container still counts. */
async function idsByLabel(runner: Runner, label: string): Promise<string[] | null> {
  const r = await runner.run(`docker ps -aq --filter ${shq(`label=${label}`)}`, {
    label: 'docker ps',
  });
  // null, never []: "docker did not answer" is a different fact from "nothing is running", and
  // collapsing them is how a UI reports a live stack as torn down.
  if (!r.ok) return null;
  return r.stdout.split('\n').map((l) => l.trim()).filter(Boolean);
}

/** Keep only Traefik labels, and redact the values — a basicAuth users label is a credential. */
function traefikLabelsOf(labels: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(labels)) {
    if (k.startsWith('traefik.')) out[k] = redactText(v);
  }
  return out;
}

/** `2026-08-21T10:00:00.123456789Z` → epoch ms, or null. Docker's zero time (`0001-01-01…`) is null too. */
function epochMs(iso: string | undefined): number | null {
  if (!iso || iso.startsWith('0001-')) return null;
  const t = Date.parse(iso);
  return Number.isFinite(t) ? t : null;
}

function toContainer(raw: RawInspect, stack?: string): ContainerInfo {
  const labels = raw.Config?.Labels ?? {};
  // Compose stamps the service name; swarm stamps `<stack>_<service>`.
  const swarmService = labels[SERVICE_LABEL];
  const service =
    labels['com.docker.compose.service'] ??
    (swarmService && stack && swarmService.startsWith(`${stack}_`) ? swarmService.slice(stack.length + 1) : swarmService) ??
    null;
  const ports: PortMap[] = [];
  for (const [spec, bindings] of Object.entries(raw.NetworkSettings?.Ports ?? {})) {
    const [portStr, protocol = 'tcp'] = spec.split('/');
    const containerPort = Number(portStr);
    if (!Number.isFinite(containerPort)) continue;
    if (bindings && bindings.length > 0) {
      for (const b of bindings) ports.push({ containerPort, protocol, hostPort: b.HostPort });
    } else {
      ports.push({ containerPort, protocol });
    }
  }
  return {
    id: (raw.Id ?? '').slice(0, 12),
    // Docker prefixes container names with a slash.
    name: (raw.Name ?? '').replace(/^\//, ''),
    service,
    image: raw.Config?.Image ?? '',
    state: raw.State?.Status ?? 'unknown',
    health: raw.State?.Health?.Status ?? null,
    exitCode: typeof raw.State?.ExitCode === 'number' ? raw.State.ExitCode : null,
    restartCount: typeof raw.RestartCount === 'number' ? raw.RestartCount : 0,
    networks: Object.keys(raw.NetworkSettings?.Networks ?? {}),
    ingressIp:
      (raw.NetworkSettings?.Networks?.[INGRESS] as { IPAddress?: string } | undefined)?.IPAddress ||
      null,
    ports,
    traefikLabels: traefikLabelsOf(labels),
    startedAt: epochMs(raw.State?.StartedAt),
    node: labels[NODE_LABEL] ? 'this node' : null,
    remote: false,
  };
}

// ── swarm services and tasks ────────────────────────────────────────────────────────────────────

/** `docker service inspect` output, the fields read here. */
type RawService = {
  ID?: string;
  UpdatedAt?: string;
  CreatedAt?: string;
  Spec?: {
    Name?: string;
    Labels?: Record<string, string>;
    TaskTemplate?: { ContainerSpec?: { Image?: string }; Networks?: Array<{ Target?: string }> };
  };
};

export type SwarmService = {
  id: string;
  /** Full swarm name, `<stack>_<service>`. */
  name: string;
  /** The compose service name (the part after `<stack>_`). */
  service: string;
  stack: string | null;
  image: string;
  networks: string[];
  traefikLabels: Record<string, string>;
  updatedAt: number | null;
};

type RawTask = { ID?: string; Name?: string; Image?: string; Node?: string; DesiredState?: string; CurrentState?: string; Error?: string };

async function inspectServices(runner: Runner, ids: string[], networkNames: Map<string, string>): Promise<SwarmService[]> {
  if (ids.length === 0) return [];
  const r = await runner.run(`docker service inspect ${ids.map(shq).join(' ')}`, { label: 'docker service inspect' });
  if (!r.ok) return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(r.stdout || '[]');
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  return (parsed as RawService[]).map((raw) => {
    const labels = raw.Spec?.Labels ?? {};
    const stack = labels[STACK_LABEL] ?? null;
    const name = raw.Spec?.Name ?? '';
    return {
      id: (raw.ID ?? '').slice(0, 12),
      name,
      service: stack && name.startsWith(`${stack}_`) ? name.slice(stack.length + 1) : name,
      stack,
      image: raw.Spec?.TaskTemplate?.ContainerSpec?.Image ?? '',
      networks: (raw.Spec?.TaskTemplate?.Networks ?? [])
        .map((n) => (n.Target ? networkNames.get(n.Target) ?? networkNames.get(n.Target.slice(0, 12)) ?? n.Target : ''))
        .filter(Boolean),
      traefikLabels: traefikLabelsOf(labels),
      updatedAt: epochMs(raw.UpdatedAt ?? raw.CreatedAt),
    };
  });
}

/** Network id → name, so a service's `Networks[].Target` can be read by a human and checked for the ingress. */
async function networkNames(runner: Runner): Promise<Map<string, string>> {
  const r = await runner.run(`docker network ls --format '{{.ID}} {{.Name}}'`, { label: 'docker network ls' });
  const out = new Map<string, string>();
  if (!r.ok) return out;
  for (const line of r.stdout.split('\n')) {
    const [id, name] = line.trim().split(/\s+/);
    if (id && name) out.set(id, name);
  }
  return out;
}

/** `docker stack ps`, running-desired tasks only. Null when docker did not answer. */
async function stackTasks(runner: Runner, stack: string): Promise<RawTask[] | null> {
  const r = await runner.run(
    `docker stack ps ${shq(stack)} --no-trunc --filter desired-state=running --format '{{json .}}'`,
    { label: 'docker stack ps' },
  );
  // "Nothing found in stack" is an exit 1 with nothing running — not a failure to answer.
  if (!r.ok) return /nothing found/i.test(r.stderr + r.stdout) ? [] : null;
  const tasks: RawTask[] = [];
  for (const line of r.stdout.split('\n')) {
    if (!line.trim()) continue;
    try {
      tasks.push(JSON.parse(line) as RawTask);
    } catch {
      /* a non-JSON line from an older CLI — skip it, the rest still parse */
    }
  }
  return tasks;
}

/**
 * `Running 3 minutes ago` → `running`; `Complete 2 minutes ago` → `exited`.
 *
 * A FAILED or REJECTED task whose desired state is still `running` is one swarm is about to replace
 * — the swarm equivalent of a container in `restarting`, and reported as that, so the readiness
 * watcher keeps converging instead of settling on a verdict swarm itself has not reached. A crash
 * loop under swarm is still caught: the watch times out, and the task's error is in its name.
 */
function taskState(current: string | undefined): string {
  const word = (current ?? '').trim().split(/\s+/)[0]?.toLowerCase() ?? '';
  if (word === 'running') return 'running';
  if (word === 'failed' || word === 'rejected') return 'restarting';
  if (word === 'shutdown' || word === 'complete') return 'exited';
  return word || 'unknown';
}

/**
 * The arguments of every `<name>(…)` call in a Traefik rule, by name.
 *
 * A scanner rather than a regexp, because a `HostRegexp` argument routinely contains parentheses —
 * `([a-z0-9-]*[a-z0-9])?` — and `\(([^)]*)\)` stops at the first one, handing back half a pattern
 * that compiles to nothing. Backticks delimit the argument; nothing inside them counts.
 */
function ruleArgs(rule: string, name: string): string[] {
  const out: string[] = [];
  let from = 0;
  for (;;) {
    const at = rule.indexOf(`${name}(`, from);
    if (at === -1) break;
    // Only a whole matcher name: `HostRegexp(` must not be read as `Host(`.
    if (at > 0 && /[A-Za-z]/.test(rule[at - 1]!)) {
      from = at + 1;
      continue;
    }
    let i = at + name.length + 1;
    let depth = 1;
    let quote: string | null = null;
    const start = i;
    for (; i < rule.length && depth > 0; i++) {
      const c = rule[i]!;
      if (quote) {
        if (c === quote) quote = null;
      } else if (c === '`' || c === '"' || c === "'") quote = c;
      else if (c === '(') depth++;
      else if (c === ')') depth--;
    }
    if (depth !== 0) break;
    out.push(rule.slice(start, i - 1));
    from = i;
  }
  return out;
}

/** Hostnames out of a Traefik rule. `Host(` a`,` b`)` yields both; a HostRegexp yields its pattern. */
export function hostsFromRule(rule: string): string[] {
  const hosts: string[] = [];
  for (const arg of ruleArgs(rule, 'Host')) {
    for (const h of arg.split(',')) {
      const cleaned = h.trim().replace(/^[`'"]|[`'"]$/g, '');
      if (cleaned) hosts.push(cleaned);
    }
  }
  for (const arg of ruleArgs(rule, 'HostRegexp')) {
    const cleaned = arg.trim().replace(/^[`'"]|[`'"]$/g, '');
    if (cleaned) hosts.push(`(pattern) ${cleaned}`);
  }
  return hosts;
}

/**
 * Turn one container's Traefik labels into routers joined to their service ports.
 *
 * A router with no explicit `.service` is left null rather than guessed: Traefik's own defaulting
 * depends on how many services that container defines, and a UI that guesses wrong is worse than one
 * that says "not declared".
 */
export function routesFromLabels(
  container: string,
  labels: Record<string, string>,
  ingressIp: string | null = null,
): RouteInfo[] {
  const routers = new Map<string, Partial<RouteInfo>>();
  const servicePorts = new Map<string, number>();

  for (const [key, value] of Object.entries(labels)) {
    const rm = /^traefik\.http\.routers\.([^.]+)\.(.+)$/.exec(key);
    if (rm) {
      const name = rm[1]!;
      const prop = rm[2]!;
      const r = routers.get(name) ?? {};
      if (prop === 'rule') r.rule = value;
      else if (prop === 'service') r.service = value;
      else if (prop === 'entrypoints') r.entrypoints = value;
      else if (prop === 'tls') r.tls = value === 'true';
      else if (prop === 'tls.certresolver') r.certresolver = value;
      else if (prop === 'priority') r.priority = value;
      routers.set(name, r);
      continue;
    }
    const sm = /^traefik\.http\.services\.([^.]+)\.loadbalancer\.server\.port$/.exec(key);
    if (sm) {
      const port = Number(value);
      if (Number.isFinite(port)) servicePorts.set(sm[1]!, port);
    }
  }

  return [...routers.entries()].map(([router, r]) => {
    // `|| null` rather than `?? null`: an empty `.service=` label is present-but-useless, and `??`
    // would let '' through as a service name — which then indexed the port map with '' and yielded a
    // port of ''. Caught by the typechecker, not by review.
    const service = r.service || null;
    // Fall back to a service named after the router — the convention the example file uses — but only
    // when such a service actually declared a port. Computed once, so `port` and `target` cannot
    // disagree about which port this route uses.
    const port = (service ? servicePorts.get(service) : undefined) ?? servicePorts.get(router) ?? null;
    return {
      router,
      container,
      rule: r.rule ?? '',
      hosts: r.rule ? hostsFromRule(r.rule) : [],
      service,
      port,
      entrypoints: r.entrypoints ?? null,
      tls: r.tls ?? false,
      certresolver: r.certresolver ?? null,
      priority: r.priority ?? null,
      // Only when BOTH halves are known: with no ingress IP there is no address for Traefik to dial,
      // and with no port there is nothing to dial it on. A half-built target would read like a
      // working route.
      target: ingressIp && port !== null ? `${ingressIp}:${port}` : null,
    };
  });
}

/**
 * Which ACME challenge the host's Traefik runs, read from its own flags.
 *
 * It decides an opposite rule for per-PR routers — under HTTP-01 each one needs
 * `tls.certresolver=le`, under DNS-01 it must NOT have it or every PR orders its own wildcard and
 * burns the rate limit. Getting this from the running container rather than asking means the check
 * cannot disagree with the host.
 */
export async function detectChallenge(runner: Runner): Promise<'http01' | 'dns01' | 'unknown'> {
  const ids = await idsByLabel(runner, 'com.docker.compose.project=pstack-control');
  if (!ids || ids.length === 0) return 'unknown';
  for (const raw of await inspectIds(runner, ids)) {
    const argv = [...(raw.Config?.Cmd ?? []), ...(raw.Args ?? [])].join(' ');
    if (/dnschallenge/i.test(argv)) return 'dns01';
    if (/httpchallenge/i.test(argv)) return 'http01';
  }
  return 'unknown';
}

/**
 * Everything the deployment page needs: containers, their networks and ports, the routes their labels
 * declare, and what is wrong.
 *
 * `allRouters` is passed in (rather than fetched here) so the caller can gather it once for a page
 * that shows several deployments.
 */
export async function deploymentRuntime(args: {
  stack: string;
  runner: Runner;
  challenge?: 'http01' | 'dns01' | 'unknown';
  /** router name → container names, across the WHOLE host, for the collision check. */
  allRouters?: Map<string, string[]>;
  /** Decides which labels name this stack's containers. Default compose. */
  orchestrator?: Orchestrator;
}): Promise<Runtime> {
  const { stack, runner } = args;
  const swarm = args.orchestrator === 'swarm';
  const empty = (): Runtime => ({ stack, containers: [], routes: [], findings: [], challenge: 'unknown', reachable: false });

  const ids = await idsByLabel(runner, `${swarm ? STACK_LABEL : 'com.docker.compose.project'}=${stack}`);
  if (ids === null) return empty();

  const inspected = await inspectIds(runner, ids);
  let containers = inspected.map((raw) => toContainer(raw, stack));
  let routes: RouteInfo[];
  /**
   * What the findings are ABOUT. Under compose a container carries its own labels; under swarm the
   * labels are on the service, so the checks run once per service and the containers are its tasks.
   */
  let subjects: Array<{ name: string; traefikLabels: Record<string, string>; networks: string[]; state: string; health: string | null }>;

  if (swarm) {
    const serviceIds = await runner.run(`docker service ls -q --filter ${shq(`label=${STACK_LABEL}=${stack}`)}`, {
      label: 'docker service ls',
    });
    if (!serviceIds.ok) return empty();
    const sids = serviceIds.stdout.split('\n').map((l) => l.trim()).filter(Boolean);
    const services = sids.length ? await inspectServices(runner, sids, await networkNames(runner)) : [];
    const tasks = sids.length ? await stackTasks(runner, stack) : [];
    if (tasks === null) return empty();

    // Only the tasks swarm still wants running; a replaced task's container is a corpse, not a failure.
    const wanted = new Map(tasks.map((t) => [t.ID ?? '', t]));
    const local = inspected.map((raw) => ({ info: toContainer(raw, stack), task: raw.Config?.Labels?.[TASK_LABEL] ?? '' }));
    containers = local.filter((l) => !l.task || wanted.has(l.task)).map((l) => {
      const t = wanted.get(l.task);
      return { ...l.info, node: t?.Node ?? l.info.node };
    });
    const seenTasks = new Set(local.map((l) => l.task));
    const byName = new Map(services.map((svc) => [svc.name, svc]));
    for (const t of tasks) {
      if (!t.ID || seenTasks.has(t.ID)) continue;
      // `<stack>_<svc>.<slot>` → the service; the slot keeps replicas distinguishable.
      const svcName = (t.Name ?? '').replace(/\.[^.]+$/, '');
      const svc = byName.get(svcName);
      containers.push({
        id: t.ID.slice(0, 12),
        name: t.Name ?? t.ID.slice(0, 12),
        service: svc?.service ?? (svcName.startsWith(`${stack}_`) ? svcName.slice(stack.length + 1) : svcName || null),
        image: t.Image ?? svc?.image ?? '',
        state: taskState(t.CurrentState),
        health: null,
        // `Complete` is a one-shot that finished — exit 0 as far as swarm is concerned. Anything
        // else swarm does not say, and "unknown" must not read as a crash.
        exitCode: /^complete/i.test((t.CurrentState ?? '').trim()) ? 0 : null,
        restartCount: 0,
        networks: svc?.networks ?? [],
        ingressIp: null,
        ports: [],
        traefikLabels: svc?.traefikLabels ?? {},
        startedAt: svc?.updatedAt ?? null,
        node: t.Node ?? null,
        remote: true,
      });
    }
    routes = services.flatMap((svc) => routesFromLabels(svc.service, svc.traefikLabels, null));
    subjects = services.map((svc) => {
      const mine = containers.filter((c) => c.service === svc.service);
      return {
        name: svc.service,
        traefikLabels: svc.traefikLabels,
        networks: svc.networks,
        state: mine.some((c) => c.state === 'running') ? 'running' : mine[0]?.state ?? 'no tasks',
        health: mine.find((c) => c.health === 'unhealthy')?.health ?? mine.find((c) => c.health)?.health ?? null,
      };
    });
  } else {
    routes = containers.flatMap((c) => routesFromLabels(c.name, c.traefikLabels, c.ingressIp));
    subjects = containers.map((c) => ({
      name: c.service ?? c.name,
      traefikLabels: c.traefikLabels,
      networks: c.networks,
      state: c.state,
      health: c.health,
    }));
  }

  const challenge = args.challenge ?? (await detectChallenge(runner));
  const findings: Finding[] = [];
  const networkKey = swarm ? 'traefik.swarm.network' : 'traefik.docker.network';

  if (containers.length === 0) {
    findings.push({
      level: 'info',
      message:
        'Nothing is running for this stack. Deploy it, or check that the services you expect are ' +
        'behind a profile the spec selects — a service whose profile is not enabled is not started.',
    });
  }

  for (const c of subjects) {
    const enabled = c.traefikLabels['traefik.enable'] === 'true';
    const hasAnyTraefik = Object.keys(c.traefikLabels).length > 0;
    const cRoutes = routes.filter((r) => r.container === c.name);

    if (!hasAnyTraefik) {
      findings.push({
        level: 'warn',
        message:
          `${c.name} has no Traefik labels, so no hostname reaches it. If it is meant ` +
          `to be reachable it needs traefik.enable=true, ${networkKey}=${INGRESS}, a ` +
          `router rule, and a loadbalancer.server.port — see examples/docker-compose.preview.yml.`,
      });
    } else if (!enabled) {
      findings.push({
        level: 'error',
        message:
          `${c.name} has Traefik labels but not traefik.enable=true. The control stack ` +
          `runs with exposedbydefault=false, so Traefik ignores this ${swarm ? 'service' : 'container'} entirely — the ` +
          `hostname will 404 with nothing logged anywhere.`,
      });
    }

    if (hasAnyTraefik && !c.networks.includes(INGRESS)) {
      const lookalike = c.networks.find((n) => n.endsWith(`_${INGRESS}`));
      findings.push({
        level: 'error',
        message: lookalike
          ? `${c.name} is on "${lookalike}", not "${INGRESS}". Compose created its own ` +
            `network because the compose file declares it without \`external: true\`. The container ` +
            `is healthy and unreachable, and Traefik answers 404.`
          : `${c.name} is not attached to "${INGRESS}" (on: ${c.networks.join(', ') || 'none'}). ` +
            `Traefik dials containers over that network, so it cannot reach this one.`,
      });
    }

    if (c.networks.length > 1 && enabled && !c.traefikLabels[networkKey]) {
      findings.push({
        level: 'warn',
        message:
          `${c.name} is on ${c.networks.length} networks and does not set ` +
          `${networkKey}=${INGRESS}. Traefik has to pick one, and picking the per-project ` +
          `network yields an unreachable backend.`,
      });
    }

    if (enabled && cRoutes.length === 0) {
      findings.push({
        level: 'warn',
        message: `${c.name} is Traefik-enabled but declares no router rule, so nothing routes to it.`,
      });
    }

    if (c.state === 'running' && c.health === 'unhealthy') {
      findings.push({
        level: 'warn',
        message: `${c.name} is running but unhealthy — Traefik will not route to it while it stays that way.`,
      });
    }
  }

  for (const c of containers) {
    if (c.remote) {
      findings.push({
        level: 'info',
        message: `${c.name} runs on node ${c.node ?? '?'}. Logs reach it through the manager; a terminal and stop/start do not.`,
      });
    }
  }

  for (const r of routes) {
    if (!r.rule) {
      findings.push({ level: 'error', message: `Router "${r.router}" has no rule, so it matches nothing.` });
    }
    if (r.port === null) {
      findings.push({
        level: 'warn',
        message:
          `Router "${r.router}" has no loadbalancer.server.port, so Traefik guesses the container's ` +
          `exposed port. If the image exposes none — or several — the result is a 502.`,
      });
    }
    if (challenge === 'http01' && r.tls && !r.certresolver) {
      findings.push({
        level: 'error',
        message:
          `Router "${r.router}" requests TLS but sets no tls.certresolver, and this host uses ` +
          `HTTP-01, where every hostname resolves its own certificate. The route will exist and the ` +
          `TLS handshake will fail.`,
      });
    }
    if (challenge === 'dns01' && r.certresolver) {
      findings.push({
        level: 'warn',
        message:
          `Router "${r.router}" sets tls.certresolver on a DNS-01 host. One always-on router already ` +
          `requests the wildcard; this makes the PR order its own certificate and burn the ` +
          `~50-per-week limit. Use tls=true alone.`,
      });
    }
    // Rule 3 of the example, and the only failure here that cannot be seen from one deployment:
    // Traefik's router namespace is global, so a duplicate name means one PR serves another's app.
    const owners = args.allRouters?.get(r.router);
    if (owners && owners.length > 1) {
      findings.push({
        level: 'error',
        message:
          `Router name "${r.router}" is declared by ${owners.length} containers on this host ` +
          `(${owners.join(', ')}). Traefik's router namespace is global: these overwrite each other, ` +
          `and one hostname will serve the wrong container. Include the stack in the router name.`,
      });
    }
  }

  return { stack, containers, routes, findings, challenge, reachable: true };
}

/**
 * Every Traefik router declared by any container on the host, as router name → container names.
 *
 * Two uses: the global collision check above, and the Routing page — where "the routes Traefik
 * actually has" is what an operator means by routing, and none of it lives in the file provider.
 */
export async function allTraefikRouters(runner: Runner): Promise<{
  reachable: boolean;
  routes: Array<RouteInfo & { project: string | null }>;
  byName: Map<string, string[]>;
}> {
  const ids = await idsByLabel(runner, 'traefik.enable=true');
  if (ids === null) return { reachable: false, routes: [], byName: new Map() };

  const routes: Array<RouteInfo & { project: string | null }> = [];
  const byName = new Map<string, string[]>();
  for (const raw of await inspectIds(runner, ids)) {
    const c = toContainer(raw);
    const project = (raw.Config?.Labels ?? {})['com.docker.compose.project'] ?? null;
    for (const r of routesFromLabels(c.name, c.traefikLabels, c.ingressIp)) {
      routes.push({ ...r, project });
      byName.set(r.router, [...(byName.get(r.router) ?? []), c.name]);
    }
  }
  // Swarm services declare their routers on the SERVICE. A daemon that is not a manager answers this
  // with an error — which is "no swarm routes", not "docker did not answer".
  const svc = await runner.run(`docker service ls -q --filter 'label=traefik.enable=true'`, { label: 'docker service ls' });
  if (svc.ok) {
    const sids = svc.stdout.split('\n').map((l) => l.trim()).filter(Boolean);
    for (const s of sids.length ? await inspectServices(runner, sids, await networkNames(runner)) : []) {
      for (const r of routesFromLabels(s.name, s.traefikLabels, null)) {
        routes.push({ ...r, project: s.stack });
        byName.set(r.router, [...(byName.get(r.router) ?? []), s.name]);
      }
    }
  }
  routes.sort((a, b) => a.router.localeCompare(b.router));
  return { reachable: true, routes, byName };
}
