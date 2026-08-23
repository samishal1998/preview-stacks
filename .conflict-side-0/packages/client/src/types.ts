/**
 * The shapes this client returns.
 *
 * Hand-written rather than generated, and deliberately NOT imported from the server package: this
 * package must install without pulling in a control plane (and its Docker-shaped assumptions) as a
 * dependency. The anti-drift measure is not a shared type — it is `test/client.test.ts`, which boots
 * the REAL server and drives this client against it, so a field that changes shape fails a test
 * rather than silently lying in a `.d.ts`.
 *
 * Every type is the JSON as it arrives. Nothing is renamed on the way through, because a client that
 * renames the server's fields makes the API docs wrong for anyone reading both.
 */

export type Kind = 'isolated' | 'shared';
/** `sleep` takes the compose project down (volumes and axes stay); `wake` is `up` recorded under its own name. */
export type JobAction = 'up' | 'down' | 'verify' | 'sleep' | 'wake';
export type Orchestrator = 'compose' | 'swarm';
export type ShareView = 'details' | 'logs';

/**
 * Present while a deployment is asleep: when, why, and the hostnames a request to which wakes it
 * (captured from its Traefik labels the moment before teardown).
 */
export type SleepRecord = {
  since: number;
  reason: string;
  hosts: string[];
  /** `HostRegexp` patterns (wildcard subdomains), Go syntax. */
  rules: string[];
};
/** `cancelled` = a person stopped it; nothing it had done was undone. */
export type JobState = 'running' | 'ok' | 'failed' | 'leaked' | 'cancelled';
export type ReadinessState = 'watching' | 'ready' | 'failed' | 'timedout';

export type Health = {
  ok: boolean;
  /** False only in loopback dev mode, where the server refuses to bind off-localhost. */
  authEnforced: boolean;
  hasUsers?: boolean;
  dataDir: string;
  version: string;
};

export type DeploymentRow = {
  id: string;
  kind: Kind;
  specName?: string | null;
  /** Null when the spec could not be resolved — see `unresolved`. */
  stack: string | null;
  /**
   * `null` means the server could not determine it, NEVER "no".
   *
   * An unresolved spec has no stack name to look up, and a docker that did not answer is a different
   * fact from "nothing is running". Treating either as `false` is how a live stack gets reported as
   * torn down, so the tri-state is preserved all the way out to callers.
   */
  busy: boolean | null;
  running: boolean | null;
  /** The sleep record, or null when awake. A sleeping stack is neither running nor torn down. */
  asleep?: SleepRecord | null;
  /** `compose` or `swarm`; null when the spec has no compose section or could not be resolved. */
  orchestrator?: Orchestrator | null;
  /** The spec's `sleep:` policy as durations (`2h`), on the detail route only. */
  sleep?: { idle: string | null; after: string | null } | null;
  /** Why the spec could not be resolved — usually a variable this call did not supply. */
  unresolved?: string;
  createdAt?: number;
  updatedAt?: number;
};

export type SwarmNode = {
  id: string;
  hostname: string;
  role: 'manager' | 'worker';
  status: string;
  availability: string;
  managerStatus: string | null;
  engineVersion: string;
  /** The node this API runs on. */
  self: boolean;
};

export type SwarmInfo = {
  /** False when docker did not answer — every other field is then unknown, not empty. */
  reachable: boolean;
  /** Whether this daemon is a swarm manager. Inactive hosts run previews with compose. */
  active: boolean;
  nodeId: string | null;
  /** `<advertise-addr>:2377`, what a worker joins. */
  managerAddr: string | null;
  nodes: SwarmNode[];
  error?: string;
  /** The ports a worker must reach on the manager (and nodes on each other). */
  ports: Array<{ port: string; why: string }>;
  note: string;
};

export type ShareLink = {
  /** `https://control.<domain>/deployments/<id>/public-logs-view?token=…` */
  url: string;
  /** The JWT. Returned ONCE; stored nowhere on the server. Works as this client's `token` for the GETs it allows. */
  token: string;
  views: ShareView[];
  /** Epoch ms. */
  expiresAt: number;
};

export type StepResult = {
  axis: string;
  phase: 'requires' | 'up' | 'down' | 'assert_gone' | 'assert_live' | 'compose';
  ok: boolean;
  code: number;
  message?: string;
  skipped: boolean;
};

export type Job = {
  id: string;
  stack: string;
  action: JobAction;
  state: JobState;
  startedAt: number;
  endedAt?: number;
  outcome?: { ok: boolean; steps: StepResult[]; outputs: Record<string, string> };
  error?: string;
  log?: Array<{ seq: number; at: number; level: string; message: string }>;
  cancelledBy?: string;
};

export type ContainerReadiness = {
  name: string;
  service: string | null;
  state: string;
  health: string | null;
  /** `false` = it is running and nothing probed it. Not the same as "probed and passing". */
  hasHealthcheck: boolean;
  exitCode: number | null;
  restartCount: number;
  ready: boolean;
  failed: boolean;
  reason?: string;
};

export type Readiness = {
  stack: string;
  state: ReadinessState;
  containers: ContainerReadiness[];
  startedAt: number;
  endedAt?: number;
  /** False = docker stopped answering; the container fields are last-known, not current. */
  reachable: boolean;
  timeoutMs: number;
};

export type RuntimeContainer = {
  id: string;
  name: string;
  service: string | null;
  image: string;
  state: string;
  health: string | null;
  exitCode: number | null;
  restartCount: number;
  networks: string[];
  ingressIp: string | null;
  ports: Array<{ containerPort: number; protocol: string; hostPort?: string }>;
  traefikLabels: Record<string, string>;
  /** Epoch ms when the process started; null when docker did not say. */
  startedAt?: number | null;
  /** The swarm node it runs on, or null under compose. */
  node?: string | null;
  /** A swarm task on ANOTHER node: listed, but out of reach of exec/stop from the manager. */
  remote?: boolean;
};

export type RuntimeRoute = {
  router: string;
  container: string;
  rule: string;
  hosts: string[];
  service: string | null;
  port: number | null;
  entrypoints: string | null;
  tls: boolean;
  certresolver: string | null;
  priority: string | null;
  target: string | null;
};

export type Runtime = {
  id?: string;
  orchestrator?: Orchestrator | null;
  asleep?: SleepRecord | null;
  stack: string;
  containers: RuntimeContainer[];
  routes: RuntimeRoute[];
  findings: Array<{ level: 'error' | 'warn' | 'info'; message: string }>;
  challenge: 'http01' | 'dns01' | 'unknown';
  reachable: boolean;
};

export type Logs = {
  stack: string;
  tail: number;
  service: string | null;
  timestamps?: boolean;
  since?: string | null;
  until?: string | null;
  lines?: number;
  /** False = compose exited non-zero; `text` still carries whatever it printed. */
  ok: boolean;
  text: string;
};

export type SpecMeta = {
  name: string;
  kind: Kind;
  description?: string;
  requiredVars?: string[];
  usedBy?: string[];
  updatedAt?: number;
};

export type NotifierRow = {
  id: number;
  type: string;
  name: string;
  /** Secret-marked fields arrive masked; there is no read path for the real value. */
  config: Record<string, unknown>;
  events: string[];
  enabled: boolean;
  lastStatus?: string | null;
  lastAt?: number | null;
};

export type DeliveryRow = {
  id: number;
  notifierId: number;
  eventId: string;
  event: string;
  status: string;
  attempts: number;
  responseCode: number | null;
  error: string | null;
  createdAt: number;
  updatedAt: number;
  /** The envelope was stored, so this delivery can be replayed. */
  replayable?: boolean;
};

export type HostVar = {
  name: string;
  /** Null for a secret — the server has no route that returns one. */
  value: string | null;
  secret: boolean;
  updatedAt: number;
};

/** The four fields a webhook receiver gets. `data` is documented per event in docs/webhook-events.md. */
export type PstackEvent = {
  id: string;
  event: string;
  at: number;
  data: Record<string, unknown>;
};
