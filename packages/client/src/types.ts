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
/**
 * A job's state.
 *
 * Two of these mean it has NOT run yet, and both are easy to mistake for an answer:
 *
 *  - `queued` — accepted, with an id, waiting its turn. A stack runs one job at a time, and the
 *    host runs at most `PSTACK_MAX_JOBS` across every stack; over either limit a job waits rather
 *    than being refused. It has no `startedAt`.
 *  - `superseded` — it was queued and a newer job for the same stack replaced it. The queue is one
 *    deep, so five rapid pushes run the first deploy and then exactly one more carrying the newest
 *    spec; the three in between end here. Nothing ran, so unlike `cancelled` there is no partial
 *    state to clean up.
 *
 * `cancelled` = a person stopped it. If it had started, whatever it had already done was NOT undone.
 */
export type JobState = 'queued' | 'running' | 'ok' | 'failed' | 'leaked' | 'cancelled' | 'superseded';

/**
 * The states a job never leaves. Exported because "is this over?" is the question every caller of
 * this SDK asks, and `state !== 'running'` — the obvious way to ask it — answers YES for a queued
 * job that has not started. `waitForJob` uses this list.
 */
export const TERMINAL_JOB_STATES: readonly JobState[] = ['ok', 'failed', 'leaked', 'cancelled', 'superseded'];
export type ReadinessState = 'watching' | 'ready' | 'failed' | 'timedout';

export type Health = {
  ok: boolean;
  /** False only in loopback dev mode, where the server refuses to bind off-localhost. */
  authEnforced: boolean;
  hasUsers?: boolean;
  /**
   * The ENABLED sign-in providers, or null when there are none. Readable BEFORE authenticating —
   * a login page needs one button per entry. `key` is what `/api/auth/sso/start?provider=` takes;
   * `preset` is the preset the config came from (`''` for a bare OIDC issuer), for an icon.
   */
  sso?: { providers: Array<{ key: string; label: string; preset: string }> } | null;
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
  /** `null` while the job is `queued`, and on a `superseded` one — it never started. */
  startedAt: number | null;
  endedAt?: number;
  outcome?: { ok: boolean; steps: StepResult[]; outputs: Record<string, string> };
  error?: string;
  log?: Array<{ seq: number; at: number; level: string; message: string }>;
  cancelledBy?: string;
};

/** The four fields a 202 carries — `state` is `running` when it dispatched, `queued` when it waits. */
export type JobStub = { id: string; stack: string; action: JobAction; state: JobState };

/**
 * `POST /api/deployments/:id/cancel` — stop everything this deployment's stack has outstanding: the
 * running job AND the one queued behind it, in one call.
 *
 * NOT a teardown. It stops work and destroys nothing; it also undoes nothing a running job had
 * already done. `cancelled` is `[]`, never null, when there was nothing outstanding, and `warning`
 * differs by whether anything had actually started — a queued job leaves no partial state behind.
 */
export type CancelStack = {
  stack: string;
  cancelled: JobStub[];
  by: string;
  warning: string;
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
  /** A one-shot swarm service's synthetic row (0.31.0+): container verbs have nothing to act on. */
  job?: boolean;
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

// ── accounts and roles ────────────────────────────────────────────────────────────────────────────

/**
 * The four ordered roles, weakest first — each includes everything below it:
 *
 *   viewer      every read
 *   developer   + stacks and deployments (up/down/verify/sleep/wake, containers, specs, terminal)
 *   maintainer  + host configuration (host vars, registries, routing, notifiers, swarm join)
 *   admin       + people, and anything that can mint them (users, SSO config, sealed config)
 *
 * `root` — the `PSTACK_TOKEN` bearer — sits ABOVE all four and is not one of them. A share link is
 * not a role at any rank: it reaches the reads its own deployment allows and nothing else.
 */
export type Role = 'viewer' | 'developer' | 'maintainer' | 'admin';

/** An account, as every response carries it. */
export type User = {
  id: number;
  username: string;
  /**
   * `string`, not `Role`, on the way OUT: `users.role` is a plain TEXT column an operator is
   * expected to be able to repair over SSH, so a value outside the four is possible. The server
   * ranks such a role BELOW viewer — read an unfamiliar one as *less* than viewer, never as more.
   * What you SEND is a `Role`; anything else is a 400.
   */
  role: string;
  /** From an SSO provider's claims; null for every locally-created account. */
  email: string | null;
  createdAt: number;
};

/** Who a token is. `user` is present only for an account, `share` only for a share link. */
export type Me = {
  /** The `PSTACK_TOKEN` bearer — above every role, and it carries no account. */
  root: boolean;
  user?: User;
  /** Epoch ms, or absent when the link could not be re-verified. */
  share?: { deployment: string; views: ShareView[]; expiresAt?: number | null };
};

// ── single sign-on ────────────────────────────────────────────────────────────────────────────────

export type SsoClaimMap = { subject: string; username: string; email: string; name: string; avatar: string };

/** The stored provider. The client secret is NEVER part of this — it has no read path. */
export type SsoConfig = {
  mode: 'oidc' | 'oauth2';
  enabled: boolean;
  /** Button text, and the `providerKey` half of an account's identity. */
  label: string;
  clientId: string;
  /** Mode A: the issuer, or a full `.well-known` URL. */
  discoveryUrl: string;
  /** Mode B: a preset key, or `custom`. */
  provider: string;
  authorizeUrl: string;
  tokenUrl: string;
  userInfoUrl: string;
  /** Consulted only when the profile carries no address (GitHub's default). */
  emailsUrl: string;
  /** Where the provider lists this user's groups/orgs. Preset-filled; only read when `requiredGroups` is set. */
  groupsUrl: string;
  scopes: string;
  claimMap: SsoClaimMap;
  /**
   * The three sign-in rules. Each list is ANY-OF, and the three are ANDed with each other. An empty
   * list is no constraint; a non-empty one FAILS CLOSED — a login the rule cannot be evaluated
   * against is refused, not waved through.
   */
  allowedEmailDomains: string[];
  /**
   * Glob patterns (`path.Match` semantics, so `*`, `?` and `[a-z]` all work), matched case-folded
   * against the provider's username. Inert on a provider that supplies no username claim, which
   * many bare OIDC providers do not — and inert here means every login is refused, not admitted.
   */
  allowedUsernames: string[];
  /**
   * Group/org names, matched exactly and case-folded. Setting this requires a provider whose preset
   * knows a groups endpoint, and scopes that can read it — the server refuses the save otherwise
   * rather than letting every later login fail.
   */
  requiredGroups: string[];
  /**
   * The role an account this provider MINTS is created at. Deliberately not narrowed to `Role`: the
   * server stores this string without validating it, and one it does not recognise ranks below
   * viewer. Whatever it says is the floor every person who signs in through this provider gets.
   */
  defaultRole: string;
};

export type SsoPreset = {
  key: string;
  label: string;
  /** How the provider is talked to; the config's mode defaults from it. */
  mode: 'oidc' | 'oauth2';
  /** Login-page button text ("Continue with GitHub"). */
  buttonLabel: string;
  /** Where the operator registers the OAuth app, and the walkthrough to show beside the form. */
  setupUrl: string;
  setupHint: string;
  /**
   * The issuer, for an oidc preset (`''` otherwise). One containing `<` is a TEMPLATE — render it
   * as a field for the operator to fill in; the server refuses it verbatim.
   */
  discoveryUrl: string;
  authorizeUrl: string;
  tokenUrl: string;
  userInfoUrl: string;
  scopes: string;
  claimMap: SsoClaimMap;
};

/** One stored provider, under its operator-chosen slug. */
export type SsoProviderEntry = {
  key: string;
  config: SsoConfig;
  /** All a read learns about the client secret — the value has no read path. */
  secretSet: boolean;
  updatedAt: number;
};

export type SsoConfigResponse = {
  /** Every stored provider, in key order — disabled ones included. */
  providers: SsoProviderEntry[];
  /** Register THIS with every provider, byte for byte. Built server-side; never guess it. */
  callbackUrl: string;
  presets: SsoPreset[];
};
