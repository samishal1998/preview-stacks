/**
 * Docker Swarm: the compose→swarm conversion, the `docker stack` command lines, and the node/join
 * helpers behind the swarm panel.
 *
 * ── WHY SWARM AT ALL ─────────────────────────────────────────────────────────────────────────────
 *
 * One box runs out. The cheapest way to add a second without changing what a spec author writes is
 * swarm: the manager is the box you already have, a worker is `docker swarm join` on another, and
 * `docker stack deploy` schedules a preview's services across whichever nodes have room. Nothing else
 * in the product changes — the axes, the leak gate and the registry are exactly as they were, because
 * they never cared which daemon a container landed on.
 *
 * ── THE CONVERSION, AND WHY IT IS AUTOMATIC ──────────────────────────────────────────────────────
 *
 * `docker stack deploy` reads the compose v3 schema, STRICTLY: `mem_limit`, `cpus`, `profiles`,
 * `pull_policy` and friends are "Additional property … is not allowed" — a hard error, not a warning —
 * and `restart:` is silently ignored, which under swarm's default restart policy (`any`) turns a
 * one-shot migration container into an infinite loop. Asking every spec author to learn that list is
 * how previews stop getting written. So pstack converts the submitted file on every invocation
 * (the same pipeline that generates Traefik labels — see autolabel.ts) and reports what it changed in
 * the job log. The original file is never modified.
 *
 * Every rule in `swarmify` is FAITHFUL: the converted file means what the plain one meant under
 * compose. A missing `restart:` becomes `condition: none` (compose would not restart it either), not
 * swarm's default. Where there is no faithful translation (`privileged`, `devices`, `build`,
 * `container_name`) the key is dropped and named, because a deploy that fails with "privileged is not
 * allowed" teaches less than one that says "dropped privileged from service worker — swarm cannot run
 * privileged tasks; pin this deployment to compose with `compose.orchestrator: compose`".
 *
 * Traefik labels move to `deploy.labels`: the swarm provider reads SERVICE labels, and leaving them on
 * the container too would make the docker provider (still on, for the control stack) build a second
 * copy of every router. `traefik.docker.network` becomes `traefik.swarm.network` for the same reason.
 *
 * ── CEILINGS, STATED ─────────────────────────────────────────────────────────────────────────────
 *
 *   - `docker stack rm` never removes volumes. Teardown removes the stack's labelled volumes on the
 *     MANAGER; a volume a task created on a worker stays there. (ponytail: worker volumes are a
 *     known leak; an axis with `docker -H ssh://worker volume rm` is the upgrade path.)
 *   - `docker exec`, `stop`, `inspect` are node-local. A task on a worker is listed (from
 *     `docker stack ps`) but the terminal and per-container actions refuse it by name.
 *   - Relative bind mounts resolve against the compose file's directory ON EACH NODE. A preview that
 *     bind-mounts `./data` works on the manager and fails on a worker that has no such directory.
 *     Use named volumes.
 */

import { shq } from './compose.ts';
import type { Runner } from './exec.ts';
import { labelsToMap } from './autolabel.ts';

/** The label swarm stamps on every task container, network and volume of a stack. */
export const STACK_LABEL = 'com.docker.stack.namespace';
/** Service name label on a task container: `<stack>_<service>`. */
export const SERVICE_LABEL = 'com.docker.swarm.service.name';
export const TASK_LABEL = 'com.docker.swarm.task.id';
export const NODE_LABEL = 'com.docker.swarm.node.id';

/**
 * Every key the compose v3.13 schema allows under a service, verbatim from docker/cli
 * `config_schema_v3.13.json`. Anything else is a hard error at deploy time, so it is dropped here
 * with a note. `x-*` extension keys are allowed by the schema's patternProperties.
 */
const V3_SERVICE_KEYS = new Set([
  'deploy', 'build', 'cap_add', 'cap_drop', 'cgroupns_mode', 'cgroup_parent', 'command', 'configs',
  'container_name', 'credential_spec', 'depends_on', 'devices', 'dns', 'dns_search', 'domainname',
  'entrypoint', 'env_file', 'environment', 'expose', 'external_links', 'extra_hosts', 'healthcheck',
  'hostname', 'image', 'init', 'ipc', 'isolation', 'labels', 'links', 'logging', 'mac_address',
  'network_mode', 'networks', 'pid', 'ports', 'privileged', 'read_only', 'restart', 'security_opt',
  'shm_size', 'secrets', 'sysctls', 'stdin_open', 'stop_grace_period', 'stop_signal', 'tmpfs', 'tty',
  'ulimits', 'oom_score_adj', 'user', 'userns_mode', 'volumes', 'working_dir',
]);

/**
 * Schema-legal keys that swarm nonetheless cannot honour. Dropped with a note rather than passed
 * through: `docker stack deploy` prints "Ignoring unsupported options" once, to a terminal nobody is
 * watching, and the preview then runs without the thing the author asked for.
 */
const SWARM_UNSUPPORTED: Record<string, string> = {
  build: 'swarm deploys images, it does not build them — build and push the image first',
  container_name: 'swarm names task containers itself (<stack>_<service>.<slot>.<task>)',
  privileged: 'swarm cannot run privileged tasks; pin this deployment to compose with `compose.orchestrator: compose`',
  devices: 'swarm tasks cannot mount host devices; pin this deployment to compose',
  network_mode: 'swarm tasks always use overlay networking',
  depends_on: 'swarm starts services independently — give the dependent a healthcheck or a retry loop',
};

export type SwarmifyResult = {
  doc: Record<string, unknown>;
  /** One line per change, for the job log. Empty when the file was already swarm-shaped. */
  notes: string[];
};

type Dict = Record<string, unknown>;
const isDict = (v: unknown): v is Dict => !!v && typeof v === 'object' && !Array.isArray(v);

/** A labels value (list or mapping) → `k=v` list. Lists survive round-trips through every parser identically. */
function toLabelList(labels: Record<string, string>): string[] {
  return Object.entries(labels).map(([k, v]) => (v === '' ? k : `${k}=${v}`));
}

/**
 * Convert a compose document to the swarm subset. Pure; never mutates the input.
 *
 * @param profiles the spec's profiles — the services swarm will see. A service behind a profile not in
 *                 this list is dropped, exactly as compose would not start it. `--prune` on deploy then
 *                 removes a service that was selected last time and is not now, which is what
 *                 `--remove-orphans` does for compose.
 */
export function swarmify(input: Dict, opts: { profiles: string[] }): SwarmifyResult {
  const doc = JSON.parse(JSON.stringify(input)) as Dict;
  const notes: string[] = [];
  const note = (svc: string | null, what: string) => notes.push(svc ? `service ${svc}: ${what}` : what);

  if ('name' in doc) {
    delete doc.name;
    note(null, 'dropped top-level `name` — the stack name comes from the spec');
  }
  if (typeof doc.version === 'string' && /^2/.test(doc.version)) {
    note(null, `dropped \`version: ${doc.version}\` — swarm reads the v3 schema`);
    delete doc.version;
  }

  const services = isDict(doc.services) ? doc.services : {};
  const selected = new Set(opts.profiles);

  for (const [name, raw] of Object.entries(services)) {
    if (!isDict(raw)) continue;
    const svc = raw;

    // Profiles first: a dropped service needs no further conversion, and its notes would be noise.
    if (svc.profiles !== undefined) {
      const mine = Array.isArray(svc.profiles) ? svc.profiles.map(String) : [];
      if (mine.length > 0 && !mine.some((p) => selected.has(p))) {
        delete services[name];
        note(name, `not in the selected profiles (${mine.join(', ')}) — left out of the stack`);
        continue;
      }
      delete svc.profiles;
    }

    const deploy: Dict = isDict(svc.deploy) ? svc.deploy : {};
    const setIfAbsent = (obj: Dict, key: string, value: unknown): boolean => {
      if (obj[key] !== undefined) return false;
      obj[key] = value;
      return true;
    };

    // restart → restart_policy. Faithful: absent/no → none, which is what compose does.
    {
      const restart = svc.restart === undefined ? 'no' : String(svc.restart);
      const policy: Dict = isDict(deploy.restart_policy) ? deploy.restart_policy : {};
      let condition: string;
      let maxAttempts: number | undefined;
      if (restart === 'no' || restart === 'none') condition = 'none';
      else if (restart === 'always' || restart === 'unless-stopped') condition = 'any';
      else if (restart.startsWith('on-failure')) {
        condition = 'on-failure';
        const n = Number(restart.split(':')[1]);
        if (Number.isInteger(n) && n > 0) maxAttempts = n;
      } else condition = 'any';
      if (!isDict(deploy.restart_policy)) {
        policy.condition = condition;
        if (maxAttempts !== undefined) policy.max_attempts = maxAttempts;
        deploy.restart_policy = policy;
        note(
          name,
          `${svc.restart === undefined ? 'no `restart`' : `restart: ${restart}`} → deploy.restart_policy.condition: ${condition}` +
            (maxAttempts !== undefined ? ` (max_attempts ${maxAttempts})` : ''),
        );
      }
      delete svc.restart;
    }

    // Resource limits: v2/compose-spec keys → deploy.resources.
    {
      const resources: Dict = isDict(deploy.resources) ? deploy.resources : {};
      const limits: Dict = isDict(resources.limits) ? resources.limits : {};
      const reservations: Dict = isDict(resources.reservations) ? resources.reservations : {};
      const moved: string[] = [];
      const move = (from: string, into: Dict, key: string, label: string) => {
        if (svc[from] === undefined) return;
        if (setIfAbsent(into, key, typeof svc[from] === 'number' ? String(svc[from]) : svc[from])) moved.push(`${from} → ${label}`);
        delete svc[from];
      };
      move('mem_limit', limits, 'memory', 'deploy.resources.limits.memory');
      move('cpus', limits, 'cpus', 'deploy.resources.limits.cpus');
      move('pids_limit', limits, 'pids', 'deploy.resources.limits.pids');
      move('mem_reservation', reservations, 'memory', 'deploy.resources.reservations.memory');
      for (const k of ['cpu_shares', 'cpu_quota', 'cpu_period', 'cpuset', 'memswap_limit', 'mem_swappiness', 'oom_kill_disable']) {
        if (svc[k] !== undefined) {
          delete svc[k];
          note(name, `dropped \`${k}\` — no swarm equivalent (use deploy.resources)`);
        }
      }
      if (Object.keys(limits).length) resources.limits = limits;
      if (Object.keys(reservations).length) resources.reservations = reservations;
      if (Object.keys(resources).length) deploy.resources = resources;
      if (moved.length) note(name, moved.join(', '));
    }

    // Traefik labels → deploy.labels. The docker provider must not see them on the task container.
    {
      const container = labelsToMap(svc.labels);
      const service = labelsToMap(deploy.labels);
      let movedAny = false;
      let renamed = false;
      for (const [k, v] of Object.entries(container)) {
        if (!k.startsWith('traefik.')) continue;
        const key = k === 'traefik.docker.network' ? 'traefik.swarm.network' : k;
        if (key !== k) renamed = true;
        if (service[key] === undefined) service[key] = v;
        delete container[k];
        movedAny = true;
      }
      if (service['traefik.docker.network'] !== undefined) {
        service['traefik.swarm.network'] ??= service['traefik.docker.network']!;
        delete service['traefik.docker.network'];
        renamed = true;
      }
      if (movedAny) note(name, 'moved traefik.* labels to deploy.labels (the swarm provider reads service labels)');
      if (renamed) note(name, 'traefik.docker.network → traefik.swarm.network (the swarm provider reads the latter)');
      if (svc.labels !== undefined) {
        if (Object.keys(container).length) svc.labels = toLabelList(container);
        else delete svc.labels;
      }
      if (Object.keys(service).length) deploy.labels = toLabelList(service);
    }

    for (const [k, why] of Object.entries(SWARM_UNSUPPORTED)) {
      if (svc[k] !== undefined) {
        delete svc[k];
        note(name, `dropped \`${k}\` — ${why}`);
      }
    }

    for (const k of Object.keys(svc)) {
      if (k.startsWith('x-') || V3_SERVICE_KEYS.has(k)) continue;
      delete svc[k];
      note(name, `dropped \`${k}\` — not in the compose v3 schema swarm reads`);
    }

    if (Object.keys(deploy).length) svc.deploy = deploy;
  }

  return { doc, notes };
}

// ── command lines ─────────────────────────────────────────────────────────────────────────────────

/**
 * `docker stack deploy`. `--prune` is `--remove-orphans`; `--with-registry-auth` ships the manager's
 * registry credential (the one `registries.ts` manages) to workers, which otherwise cannot pull a
 * private image; `--detach=true` returns once the services exist — convergence is readiness's job,
 * same as `compose up -d`.
 */
export function stackDeployCmd(stack: string, files: string[]): string {
  return `docker stack deploy ${files.map((f) => `-c ${shq(f)}`).join(' ')} --prune --with-registry-auth --detach=true ${shq(stack)}`;
}

/**
 * `docker stack rm`, then WAIT. `stack rm` returns before the tasks and networks are gone, and a
 * redeploy that races it fails with "network … has active endpoints". Bounded at 60s.
 *
 * @param volumes also remove the stack's labelled volumes (teardown). Sleep keeps them.
 */
export function stackRmCmd(stack: string, opts: { volumes: boolean }): string {
  const s = shq(stack);
  const wait =
    `for i in $(seq 1 60); do ` +
    `[ -z "$(docker stack ps ${s} -q 2>/dev/null)" ] && ` +
    `[ -z "$(docker network ls -q --filter ${shq(`label=${STACK_LABEL}=${stack}`)})" ] && break; sleep 1; done`;
  const vols = opts.volumes
    ? `; docker volume ls -q --filter ${shq(`label=${STACK_LABEL}=${stack}`)} | xargs -r docker volume rm >/dev/null`
    : '';
  return `docker stack rm ${s}; ${wait}${vols}`;
}

export function stackPsCmd(stack: string): string {
  return `docker stack ps ${shq(stack)}`;
}

/**
 * `docker service logs`, per service. A whole-stack read loops over the stack's services; FOLLOWED,
 * they run in parallel and `wait` keeps the shell alive until every one ends.
 */
export function stackLogsCmd(
  stack: string,
  tail: number,
  service: string | undefined,
  opts: { timestamps?: boolean; since?: string; follow?: boolean },
): string {
  const flags = [
    `--no-color --tail ${Math.trunc(tail)}`,
    opts.follow ? '--follow' : '',
    opts.timestamps ? '--timestamps' : '',
    opts.since ? `--since ${shq(opts.since)}` : '',
  ].filter(Boolean).join(' ');
  if (service) return `docker service logs ${flags} ${shq(`${stack}_${service}`)}`;
  const s = shq(stack);
  const each = `docker service logs ${flags} "$svc" 2>&1${opts.follow ? ' &' : ''}`;
  return (
    `svcs=$(docker stack services --format '{{.Name}}' ${s}); ` +
    `[ -n "$svcs" ] || { echo "no services in stack ${stack}" >&2; exit 1; }; ` +
    `for svc in $svcs; do ${each}; done${opts.follow ? '; wait' : ''}`
  );
}

// ── the cluster ──────────────────────────────────────────────────────────────────────────────────

export type SwarmNode = {
  id: string;
  hostname: string;
  role: 'manager' | 'worker';
  /** `ready` / `down` / `unknown` … as docker reports it, lowercased. */
  status: string;
  availability: string;
  /** `leader` / `reachable` / `unreachable` for a manager; null for a worker. */
  managerStatus: string | null;
  engineVersion: string;
  /** The node this API runs on. */
  self: boolean;
};

export type SwarmInfo = {
  /** False when docker did not answer — every other field is then unknown, not empty. */
  reachable: boolean;
  /** Whether this daemon is a swarm manager. `inactive` hosts run compose. */
  active: boolean;
  nodeId: string | null;
  /** `<advertise-addr>:2377`, what a worker joins. */
  managerAddr: string | null;
  nodes: SwarmNode[];
  error?: string;
};

type RawSwarmInfo = {
  NodeID?: string;
  NodeAddr?: string;
  LocalNodeState?: string;
  ControlAvailable?: boolean;
  Error?: string;
  RemoteManagers?: Array<{ NodeID?: string; Addr?: string }> | null;
};

/** What the swarm panel shows. Read straight from docker every time — nothing here is cached (invariant 10). */
export async function swarmInfo(runner: Runner): Promise<SwarmInfo> {
  const info = await runner.run(`docker info --format '{{json .Swarm}}'`, { label: 'docker info' });
  if (!info.ok) return { reachable: false, active: false, nodeId: null, managerAddr: null, nodes: [] };
  let raw: RawSwarmInfo;
  try {
    raw = JSON.parse(info.stdout || '{}') as RawSwarmInfo;
  } catch {
    return { reachable: false, active: false, nodeId: null, managerAddr: null, nodes: [] };
  }
  const active = raw.LocalNodeState === 'active';
  const self = raw.NodeID ?? null;
  const manager = raw.RemoteManagers?.find((m) => m.NodeID === self)?.Addr ?? raw.RemoteManagers?.[0]?.Addr ?? null;
  const out: SwarmInfo = {
    reachable: true,
    active,
    nodeId: self,
    managerAddr: manager ?? (raw.NodeAddr ? `${raw.NodeAddr}:2377` : null),
    nodes: [],
    ...(raw.Error ? { error: raw.Error } : {}),
  };
  if (!active || !raw.ControlAvailable) return out;

  const ls = await runner.run(`docker node ls --format '{{json .}}'`, { label: 'docker node ls' });
  if (!ls.ok) return { ...out, error: ls.stderr.trim().split('\n')[0] };
  for (const line of ls.stdout.split('\n')) {
    if (!line.trim()) continue;
    let n: Record<string, string>;
    try {
      n = JSON.parse(line) as Record<string, string>;
    } catch {
      continue;
    }
    const managerStatus = (n.ManagerStatus ?? '').toLowerCase() || null;
    out.nodes.push({
      id: n.ID ?? '',
      hostname: n.Hostname ?? '',
      role: managerStatus ? 'manager' : 'worker',
      status: (n.Status ?? 'unknown').toLowerCase(),
      availability: (n.Availability ?? '').toLowerCase(),
      managerStatus,
      engineVersion: n.EngineVersion ?? '',
      self: n.Self === 'true' || n.ID === self,
    });
  }
  return out;
}

/** The worker join token. A SECRET: whoever holds it can add a node that runs any task. */
export async function workerJoinToken(runner: Runner): Promise<string | null> {
  const r = await runner.run('docker swarm join-token -q worker', { label: 'docker swarm join-token' });
  const tok = r.stdout.trim();
  return r.ok && tok ? tok : null;
}

/**
 * The ports a worker must reach on the manager (and managers on each other), from Docker's own
 * swarm docs. Stated once here so the panel, the script and the docs agree.
 */
export const SWARM_PORTS = [
  { port: '2377/tcp', why: 'cluster management (worker → manager)' },
  { port: '7946/tcp+udp', why: 'node discovery (every node ↔ every node)' },
  { port: '4789/udp', why: 'overlay network traffic — VXLAN (every node ↔ every node)' },
] as const;

export function joinCommand(token: string, managerAddr: string): string {
  return `docker swarm join --token ${token} ${managerAddr}`;
}

/**
 * A shell script that installs Docker (the vendor convenience script — the same one every quickstart
 * uses; distro-exact installs are the cloud-config's job) and joins. `set -e` so a failed install
 * never reaches the join with nothing to join.
 */
export function joinScript(token: string, managerAddr: string): string {
  return [
    '#!/usr/bin/env bash',
    '# Join this machine to the pstack swarm as a WORKER. Run as root (or with sudo).',
    `# Open ${SWARM_PORTS.map((p) => p.port).join(', ')} between this machine and the manager first.`,
    'set -euo pipefail',
    'if ! command -v docker >/dev/null 2>&1; then',
    '  curl -fsSL https://get.docker.com | sh',
    'fi',
    'systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true',
    `if [ "$(docker info --format '{{.Swarm.LocalNodeState}}')" = "active" ]; then`,
    '  echo "already part of a swarm: $(docker info --format \'{{.Swarm.NodeID}}\')"; exit 0',
    'fi',
    joinCommand(token, managerAddr),
    'docker info --format \'joined as {{.Swarm.NodeID}} ({{.Swarm.LocalNodeState}})\'',
    '',
  ].join('\n');
}
