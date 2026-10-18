/**
 * Pure rules for the deployment pages: what state to show, why a control is blocked, how a 409 is
 * read, how a compose log line splits, which URL belongs to a container. No Vue, no DOM.
 */
import type { DeploymentRow, Runtime, RuntimeContainer, RuntimeRoute } from '@samyx/preview-stacks-client';

export type Tone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'info';
export type LifecycleAction = 'up' | 'verify' | 'down' | 'sleep' | 'wake';

// ── errors ──────────────────────────────────────────────────────────────────────────────────────

/** One line for a failed call. 401/403/404 get fixed wording so a role problem never reads as a bug. */
export function problemText(status: number, error: string | undefined, what: string): string {
  if (status === 0) return error ?? 'The server did not answer.';
  if (status === 401) return 'Signed out.';
  if (status === 403) return `${what}: not allowed for your role.`;
  if (status === 404 && (!error || error === 'not found')) return `${what}: not found.`;
  return error ? `${what}: ${error}` : `${what}: HTTP ${status}`;
}

export type ConflictBody = { error?: string; stack?: string; kind?: string; containers?: number; deployments?: string[] };
export type Conflict = { kind: 'shared' | 'containers' | 'referenced' | 'other'; text: string; stack: string; deployments: string[] };

/** A 409 read by the shape of its payload, never by its message text. */
export function classifyConflict(b: ConflictBody): Conflict {
  const text = b.error ?? 'HTTP 409';
  const stack = b.stack ?? '';
  if (b.kind) return { kind: 'shared', text, stack, deployments: [] };
  if (b.containers !== undefined) return { kind: 'containers', text, stack, deployments: [] };
  if (b.deployments) return { kind: 'referenced', text, stack, deployments: b.deployments };
  return { kind: 'other', text, stack, deployments: [] };
}

// ── state ───────────────────────────────────────────────────────────────────────────────────────

type StateFields = Pick<DeploymentRow, 'busy' | 'running' | 'asleep'>;

/** busy and running are tri-state: null is "could not tell", which is not the same as idle. */
export function runState(d: StateFields): { label: string; tone: Tone } {
  if (d.busy === true) return { label: 'Busy', tone: 'info' };
  if (d.asleep) return { label: 'Asleep', tone: 'neutral' };
  if (d.running === true) return { label: 'Running', tone: 'success' };
  if (d.busy == null || d.running == null) return { label: 'Unknown', tone: 'warning' };
  return { label: 'Idle', tone: 'neutral' };
}

/** Why this action is blocked right now, or '' when it is not. */
export function whyDisabled(
  action: LifecycleAction,
  d: (Pick<DeploymentRow, 'kind' | 'busy' | 'asleep' | 'orchestrator' | 'stack'>) | null,
  forceArmed = false,
): string {
  if (!d || !d.stack) return 'Needs variables.';
  if (d.busy === true && action !== 'down') return `A job is running or waiting on ${d.stack}.`;
  if (action === 'down' && d.kind === 'shared' && !forceArmed) return `Type ${d.stack} to confirm.`;
  if (action === 'sleep' && d.kind === 'shared') return 'Shared stacks do not sleep.';
  if (action === 'sleep' && !d.orchestrator) return 'No compose section.';
  if (action === 'sleep' && d.asleep) return 'Already asleep.';
  return '';
}

// ── runtime ─────────────────────────────────────────────────────────────────────────────────────

const LEVEL_ORDER = { error: 0, warn: 1, info: 2 } as const;
export type Finding = Runtime['findings'][number];

export function sortFindings(f: Finding[]): Finding[] {
  return [...f].sort((a, b) => LEVEL_ORDER[a.level] - LEVEL_ORDER[b.level]);
}
export const findingTone = (l: Finding['level']): Tone => (l === 'error' ? 'danger' : l === 'warn' ? 'warning' : 'info');
export const findingLabel = (l: Finding['level']): string => (l === 'error' ? 'Blocking' : l === 'warn' ? 'Worth checking' : 'Note');

/** Real containers plus a synthetic row for each swarm service whose tasks all failed. */
export function containersWithServices(r: Runtime): RuntimeContainer[] {
  const have = new Set(r.containers.map((c) => c.service));
  const taskless: RuntimeContainer[] = (r.services ?? [])
    .filter((s) => !have.has(s))
    .map((s) => ({
      id: `service:${s}`,
      name: s,
      service: s,
      image: '',
      state: 'no task running',
      health: null,
      exitCode: null,
      restartCount: 0,
      networks: [],
      ingressIp: null,
      ports: [],
      traefikLabels: {},
      startedAt: null,
      node: null,
      remote: false,
      job: false,
    }));
  return [...r.containers, ...taskless].sort((a, b) => (a.service ?? a.name).localeCompare(b.service ?? b.name));
}

/** `stack_svc.1.abcd` -> `1.abcd`; compose names hold no dots and come back untouched. */
export function taskOf(name: string): string {
  const p = name.split('.');
  return p.length === 3 ? `${p[1]}.${p[2]}` : name;
}

/** The hostname a router sends to this container, or null. Pattern hosts have no single address. */
export function urlFor(r: Pick<Runtime, 'routes'> | null, c: Pick<RuntimeContainer, 'name' | 'service'>): string | null {
  const host = r?.routes
    .filter((x) => x.container === c.name || x.container === c.service)
    .flatMap((x) => x.hosts)
    .find((h) => !h.startsWith('(pattern)'));
  return host ? `https://${host}` : null;
}

/** The address Traefik assembled, or the one reason it could not. Amber = probably fix, red = broken, grey = normal. */
export function routeTarget(r: Pick<RuntimeRoute, 'target' | 'targetReason'>): { text: string; tone: Tone; hint?: string } {
  if (r.target) return { text: r.target, tone: 'neutral' };
  if (r.targetReason === 'no-port') return { text: 'No port declared', tone: 'warning' };
  if (r.targetReason === 'not-on-ingress') return { text: 'Not on the ingress network', tone: 'danger' };
  if (r.targetReason === 'unknown-node') return { text: 'Address not known from this node', tone: 'neutral', hint: 'A swarm task on another node. Normal.' };
  return { text: 'Not known', tone: 'neutral' };
}

/** Container state for a badge. Health only counts while running; Docker keeps the last probe on a stopped one. */
export function containerState(c: Pick<RuntimeContainer, 'state' | 'health'>): { label: string; tone: Tone } {
  const label = c.state === 'running' && c.health ? `${sentence(c.state)} · ${c.health}` : sentence(c.state);
  if (c.state === 'running') return { label, tone: c.health === 'unhealthy' ? 'danger' : 'success' };
  if (c.state === 'exited') return { label, tone: 'neutral' };
  return { label, tone: c.state === 'no task running' ? 'danger' : 'warning' };
}

export const sentence = (s: string): string => (s ? s[0]!.toUpperCase() + s.slice(1).replace(/-/g, ' ') : s);

// ── logs ────────────────────────────────────────────────────────────────────────────────────────

const PREFIX = /^(\S+)\s+\| /;
const TS = /^(\d{4}-\d{2}-\d{2}T[\d:.]+Z?)\s/;

/** Compose prefixes `name  | `; with --timestamps the stamp follows. A line with neither comes back as is. */
export function parseLogLine(line: string): { service?: string; time?: string; message: string } {
  const m = PREFIX.exec(line);
  if (!m) return { message: line };
  let rest = line.slice(m[0].length);
  const t = TS.exec(rest);
  if (t) rest = rest.slice(t[0].length);
  return { service: m[1], ...(t ? { time: t[1] } : {}), message: rest };
}

export function logLevel(message: string): 'error' | 'warning' | 'info' {
  if (/\b(error|fatal|panic|exception)\b/i.test(message)) return 'error';
  if (/\bwarn(ing)?\b/i.test(message)) return 'warning';
  return 'info';
}

/** Keep the lines containing `needle`, case-insensitively; an empty needle keeps everything. */
export function filterLines(lines: string[], needle: string): string[] {
  const n = needle.trim().toLowerCase();
  return n ? lines.filter((l) => l.toLowerCase().includes(n)) : lines;
}

/** Keep the newest `max` lines. */
export function capLines<T>(lines: T[], max: number): T[] {
  return lines.length > max ? lines.slice(lines.length - max) : lines;
}

export function logFileName(id: string, service: string, at = new Date()): string {
  const safe = (s: string) => s.replace(/[^a-zA-Z0-9._-]/g, '_');
  return `${safe(id)}${service ? `-${safe(service)}` : ''}-${at.toISOString().slice(0, 19).replace(/[-:T]/g, '')}.log`;
}

// ── share + equivalent command ──────────────────────────────────────────────────────────────────

export type ShareViewName = 'details' | 'logs';
export const shareViews = (details: boolean, logs: boolean): ShareViewName[] => [...(details ? (['details'] as const) : []), ...(logs ? (['logs'] as const) : [])];

/** The curl line for a request. The token stays a shell variable: a rendered command ends up in screenshots. */
export function curlFor(base: string, method: string, path: string, body?: unknown): string {
  const lines = [`curl -X ${method} ${base}${path} \\`];
  if (body === undefined) {
    lines.push(`  -H 'Authorization: Bearer $PSTACK_TOKEN'`);
  } else {
    lines.push(`  -H 'Authorization: Bearer $PSTACK_TOKEN' \\`, `  -H 'Content-Type: application/json' \\`);
    lines.push(`  -d '${JSON.stringify(body, null, 2).split("'").join(`'\\''`)}'`);
  }
  return lines.join('\n');
}

export const SHARE_TTLS = [
  { value: '1h', label: '1 hour' },
  { value: '24h', label: '24 hours' },
  { value: '7d', label: '7 days' },
  { value: '30d', label: '30 days' },
];
