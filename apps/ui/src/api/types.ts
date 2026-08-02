/**
 * The pstack HTTP API, hand-typed.
 *
 * These are DELIBERATELY duplicated from packages/pstack rather than imported. This app is a
 * separate container that only ever talks to the API over HTTP — it has no build-time relationship
 * with the server, and giving it one would mean the "opt-in extra container" could no longer be
 * built, versioned or deployed independently of the package it renders.
 *
 * Two things to keep right, because getting them wrong is how a UI lies to an operator:
 *
 * 1. THE ENVELOPE IS NOT UNIFORM. `GET /api/jobs/:id` answers `{ job }`; `GET /api/deployments/:id`
 *    answers the deployment with its fields at the TOP level. Collections wrap (`{ deployments }`,
 *    `{ jobs }`, `{ specs }`). Do not infer a rule — each response type below states its own shape.
 *
 * 2. `busy` and `running` ARE TRI-STATE: `boolean | null`. `null` means "the server could not
 *    determine this" — an unresolved spec has no stack name to look up, and a docker that did not
 *    answer is not the same fact as "nothing is running". Typing either as `boolean` is how an
 *    unknown silently folds into "idle", and a live stack gets reported as torn down.
 */

export type Kind = 'isolated' | 'shared';
export type HookName = 'up' | 'assert_live' | 'down' | 'assert_gone';
export type JobAction = 'up' | 'down' | 'verify';
export type JobState = 'running' | 'ok' | 'failed' | 'leaked';
export type StepPhase = 'requires' | 'up' | 'down' | 'assert_gone' | 'assert_live' | 'compose';
export type Visibility = 'shown' | 'masked';

/** `GET /api/health` — unwrapped. */
export type Health = {
  ok: boolean;
  /**
   * false is a deliberate mode, not a misconfiguration: with no `PSTACK_TOKEN` the server binds
   * 127.0.0.1 and refuses 0.0.0.0, so an unauthenticated instance cannot be exposed by accident.
   */
  authEnforced: boolean;
  dataDir: string;
  version: string;
};

/** One row of `GET /api/deployments` → `{ deployments }`. */
export type DeploymentRow = {
  id: string;
  kind: Kind;
  createdAt: number;
  updatedAt: number;
  /** Set when the deployment references a stored spec instead of carrying its own copy. */
  specName?: string;
  /**
   * Variables STORED with the deployment (newer servers). When present, `down` resolves the same
   * stack `up` created with no query parameters at all. Absent on an older server, or on a
   * deployment submitted without any.
   */
  vars?: Record<string, string>;
  /** null when the spec could not be resolved — there is no stack name to show. */
  stack: string | null;
  busy: boolean | null;
  running: boolean | null;
  /** The 400 text from the failed resolve. It names the missing variable; render it verbatim. */
  unresolved?: string;
};

export type Axis = {
  name: string;
  /** Hook NAMES only. Bodies are never sent — a hook is a shell string that carries tokens inline. */
  hooks: HookName[];
  /** false ⇒ no `assert_gone` ⇒ `verify` CANNOT prove this axis was cleaned up. Not a pass. */
  verifiable: boolean;
};

export type Requirement = { name: string; hint: string | null };

export type DisplayVar = {
  key: string;
  /** A mask when `visibility` is 'masked'. The plaintext is never sent to this page. */
  value: string;
  visibility: Visibility;
  /** Length of the REAL value. `0` on a masked var means declared-but-never-set. */
  length: number;
};

/** `GET /api/deployments/:id` — UNWRAPPED, fields at the top level. */
export type Deployment = {
  id: string;
  kind: Kind;
  createdAt: number;
  updatedAt: number;
  stack: string;
  busy: boolean | null;
  compose: {
    file: string;
    profiles: string[];
    overlays: string[];
    /**
     * Wildcard subdomain routes, when the spec declares any. `depth: 'any'` is routing-only — no TLS
     * certificate can cover more than one label, so those hosts serve over HTTP and not HTTPS.
     * Absent on a server built before the feature, hence optional.
     */
    subdomains?: Array<{
      profile: string;
      host: string;
      depth: 'one' | 'any';
      varName: string;
      rule: string;
    }>;
  } | null;
  requires: Requirement[];
  axes: Axis[];
  /** The variables the spec DECLARES, redacted by name. Not the resolved environment. */
  env: DisplayVar[];
};

export type ControlService = { name: string; state: string; health: string; image: string };

/** `GET /api/control` — unwrapped. There are no actions here, by design; see `actionable`. */
export type Control = {
  project: string;
  /** false ⇒ docker did not answer. NOT the same as "nothing is running". */
  reachable: boolean;
  /** docker answered and the output could not be parsed. Service state is unknown this refresh. */
  parseError: boolean;
  services: ControlService[];
  managedBy: string;
  /** Always false. Read so the UI cannot claim it was not told; never trusted to be true. */
  actionable: boolean;
  note: string;
};

export type LogEvent = { seq: number; at: number; level: string; message: string };

export type StepResult = {
  /** A `requires` step puts a REQUIREMENT name here, not an axis. */
  axis: string;
  phase: StepPhase;
  ok: boolean;
  code: number;
  message?: string;
  skipped: boolean;
};

export type Outcome = { ok: boolean; steps: StepResult[]; outputs: Record<string, string> };

/** `GET /api/jobs` → `{ jobs }`; `GET /api/jobs/:id` → `{ job }`. */
export type Job = {
  id: string;
  stack: string;
  action: JobAction;
  state: JobState;
  startedAt: number;
  endedAt?: number;
  outcome?: Outcome;
  error?: string;
  log?: LogEvent[];
};

/** The 202 body from an action. */
export type JobStub = { id: string; stack: string; action: JobAction; state: JobState };

/** One row of `GET /api/specs` → `{ specs }`. Absent entirely on a server built before specs. */
export type SpecMeta = {
  name: string;
  kind: Kind;
  description?: string;
  createdAt: number;
  updatedAt: number;
  /** Variable names the spec interpolates but does not define — what a caller MUST supply. */
  requiredVars: string[];
};

export type DeploymentsResponse = { deployments: DeploymentRow[] };
export type JobsResponse = { jobs: Job[] };
export type JobResponse = { job: Job };
export type SpecsResponse = { specs: SpecMeta[] };

/**
 * `GET /api/registries`. Hostnames and usernames only — a docker `auths` entry is reversible base64, so
 * there is no read path for the secret anywhere in the API and nothing here to reveal.
 */
export type RegistryEntry = {
  registry: string;
  username: string | null;
  /** Served by a credential helper, which does not exist inside the control container. */
  viaHelper: boolean;
};
export type RegistriesResponse = {
  dir: string;
  /** False on a control stack that predates the DOCKER_CONFIG mount — the fix is `pstack init`. */
  writable: boolean;
  entries: RegistryEntry[];
  /** `credsStore` / `credHelpers` found in the file; present means "these will not work here". */
  helpers: string[];
};

/** `GET /api/deployments/:id/runtime` — what is running and what Traefik was told about it. */
export type RuntimeContainer = {
  id: string;
  name: string;
  service: string | null;
  image: string;
  state: string;
  health: string | null;
  networks: string[];
  /** The container's IP on `preview-ingress` — the address Traefik actually dials. */
  ingressIp: string | null;
  ports: Array<{ containerPort: number; protocol: string; hostPort?: string }>;
  traefikLabels: Record<string, string>;
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
  /** `<ingress-ip>:<container-port>` — null when either half is missing. */
  target: string | null;
  /** Only on the host-wide list. */
  project?: string | null;
};
export type RuntimeResponse = {
  id: string;
  stack: string;
  containers: RuntimeContainer[];
  routes: RuntimeRoute[];
  findings: Array<{ level: 'error' | 'warn' | 'info'; message: string }>;
  challenge: 'http01' | 'dns01' | 'unknown';
  /** False means "could not determine", never "nothing is running". */
  reachable: boolean;
};
export type LiveRoutesResponse = { reachable: boolean; routes: RuntimeRoute[] };

/** A notifier registration. Note the absence of a secret — the server has no read path for it. */
export type NotifierRow = {
  id: number;
  type: string;
  name: string;
  config: Record<string, unknown>;
  /** Event names, or the single entry `'*'` meaning everything including future events. */
  events: string[];
  enabled: boolean;
  createdAt: number;
  lastStatus: string | null;
  lastAt: number | null;
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
};
/**
 * `GET /api/notifiers/meta` — what the server supports. The form is rendered FROM this, so adding a
 * notifier type on the server needs no change in the UI.
 */
export type NotifierMeta = {
  events: string[];
  wildcard: string;
  types: Array<{
    kind: string;
    label: string;
    fields: Array<{ key: string; label: string; placeholder?: string; required: boolean }>;
  }>;
};

/** One file in Traefik's watched dynamic-config directory. */
export type RoutingFile = { name: string; size: number; updatedAt: number };
export type RoutingListResponse = {
  dir: string;
  /** False on a control stack that predates the API's mount — the fix is `pstack init`. */
  writable: boolean;
  files: RoutingFile[];
};
/** Content only with a token: these files hold basic-auth hashes and forward-auth URLs. */
export type RoutingReadResponse = { name: string; content?: string; sourceWithheld?: true };
/** `previous` is the in-session undo — there is no history on disk, by design. */
export type RoutingWriteResponse = { name?: string; deleted?: string; previous: string | null };

/**
 * `GET /api/deployments/:id/source` — the stored spec and compose file, so replacing is editing
 * rather than retyping. Restricted for the same reason a named spec's source is.
 */
export type DeploymentSourceResponse = {
  id: string;
  /** Set when this deployment references a stored spec — editing this copy forks from it. */
  specName: string | null;
  spec?: string;
  compose?: string | null;
  sourceWithheld?: true;
};

/**
 * `GET /api/specs/:name`. The metadata is always served; `source` only with a token, because hook
 * bodies are shell strings that routinely carry a credential inline. When it is held back the server
 * says so EXPLICITLY rather than sending an empty string — otherwise a page cannot tell "no access"
 * apart from "an empty spec".
 */
export type SpecDetail = SpecMeta & {
  source?: string;
  sourceWithheld?: true;
};
export type LogsResponse = {
  stack: string;
  tail: number;
  /** The service the output is for, or null for the whole stack. Echoed so a stale response is
   *  detectable after switching services. */
  service?: string | null;
  ok: boolean;
  text: string;
};
export type ActionResponse = { job: JobStub };
export type SubmitResponse = {
  id: string;
  kind: Kind;
  stack: string;
  specName?: string | null;
  /**
   * Other deployment ids that resolve to this same stack. Present only on a NEW deployment and only
   * when non-empty, so `[]` can never be mistaken for "not checked". Two records on one stack drive the
   * same compose project.
   */
  stackSharedWith?: string[];
};

/**
 * A 409 body. Which 409 it is must be decided on payload SHAPE, never on the message text:
 *   `kind`        → the `kind: shared` down refusal. Fix: the typed confirmation.
 *   `containers`  → DELETE refused, containers still exist. Fix: run `down` first.
 *   `deployments` → DELETE of a spec still referenced by deployments.
 *   otherwise     → a job in flight, or "docker did not answer". Shape-identical, so do not
 *                   classify them — print the server's message and never retry.
 */
export type ConflictBody = {
  error?: string;
  stack?: string;
  kind?: Kind;
  containers?: number;
  deployments?: string[];
};

/** Variable pairs as the editors hold them: an ordered list, so a blank row can exist while typing. */
export type VarPair = { k: string; v: string };
