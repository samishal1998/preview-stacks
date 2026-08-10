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
 */

import type { Runner } from './exec.ts';
import { redactText } from './redact.ts';
import { shq } from './compose.ts';

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
  State?: { Status?: string; ExitCode?: number; Health?: { Status?: string } };
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

function toContainer(raw: RawInspect): ContainerInfo {
  const labels = raw.Config?.Labels ?? {};
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
    service: labels['com.docker.compose.service'] ?? null,
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
  };
}

/** Hostnames out of a Traefik rule. `Host(` a`,` b`)` yields both; a HostRegexp yields its pattern. */
export function hostsFromRule(rule: string): string[] {
  const hosts: string[] = [];
  for (const m of rule.matchAll(/Host\(([^)]*)\)/g)) {
    for (const h of m[1]!.split(',')) {
      const cleaned = h.trim().replace(/^[`'"]|[`'"]$/g, '');
      if (cleaned) hosts.push(cleaned);
    }
  }
  for (const m of rule.matchAll(/HostRegexp\(([^)]*)\)/g)) {
    const cleaned = m[1]!.trim().replace(/^[`'"]|[`'"]$/g, '');
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
}): Promise<Runtime> {
  const { stack, runner } = args;
  const ids = await idsByLabel(runner, `com.docker.compose.project=${stack}`);
  if (ids === null) {
    return { stack, containers: [], routes: [], findings: [], challenge: 'unknown', reachable: false };
  }

  const containers = (await inspectIds(runner, ids)).map(toContainer);
  const routes = containers.flatMap((c) => routesFromLabels(c.name, c.traefikLabels, c.ingressIp));
  const challenge = args.challenge ?? (await detectChallenge(runner));
  const findings: Finding[] = [];

  if (containers.length === 0) {
    findings.push({
      level: 'info',
      message:
        'Nothing is running for this stack. Deploy it, or check that the services you expect are ' +
        'behind a profile the spec selects — a service whose profile is not enabled is not started.',
    });
  }

  for (const c of containers) {
    const enabled = c.traefikLabels['traefik.enable'] === 'true';
    const hasAnyTraefik = Object.keys(c.traefikLabels).length > 0;
    const cRoutes = routes.filter((r) => r.container === c.name);

    if (!hasAnyTraefik) {
      findings.push({
        level: 'warn',
        message:
          `${c.service ?? c.name} has no Traefik labels, so no hostname reaches it. If it is meant ` +
          `to be reachable it needs traefik.enable=true, traefik.docker.network=${INGRESS}, a ` +
          `router rule, and a loadbalancer.server.port — see examples/docker-compose.preview.yml.`,
      });
    } else if (!enabled) {
      findings.push({
        level: 'error',
        message:
          `${c.service ?? c.name} has Traefik labels but not traefik.enable=true. The control stack ` +
          `runs with exposedbydefault=false, so Traefik ignores this container entirely — the ` +
          `hostname will 404 with nothing logged anywhere.`,
      });
    }

    if (hasAnyTraefik && !c.networks.includes(INGRESS)) {
      const lookalike = c.networks.find((n) => n.endsWith(`_${INGRESS}`));
      findings.push({
        level: 'error',
        message: lookalike
          ? `${c.service ?? c.name} is on "${lookalike}", not "${INGRESS}". Compose created its own ` +
            `network because the compose file declares it without \`external: true\`. The container ` +
            `is healthy and unreachable, and Traefik answers 404.`
          : `${c.service ?? c.name} is not attached to "${INGRESS}" (on: ${c.networks.join(', ') || 'none'}). ` +
            `Traefik dials containers over that network, so it cannot reach this one.`,
      });
    }

    if (c.networks.length > 1 && enabled && !c.traefikLabels['traefik.docker.network']) {
      findings.push({
        level: 'warn',
        message:
          `${c.service ?? c.name} is on ${c.networks.length} networks and does not set ` +
          `traefik.docker.network=${INGRESS}. Traefik has to pick one, and picking the per-project ` +
          `network yields an unreachable backend.`,
      });
    }

    if (enabled && cRoutes.length === 0) {
      findings.push({
        level: 'warn',
        message: `${c.service ?? c.name} is Traefik-enabled but declares no router rule, so nothing routes to it.`,
      });
    }

    if (c.state === 'running' && c.health === 'unhealthy') {
      findings.push({
        level: 'warn',
        message: `${c.service ?? c.name} is running but unhealthy — Traefik will not route to it while it stays that way.`,
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
  routes.sort((a, b) => a.router.localeCompare(b.router));
  return { reachable: true, routes, byName };
}
