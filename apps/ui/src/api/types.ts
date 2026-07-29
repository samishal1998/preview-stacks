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
  compose: { file: string; profiles: string[]; overlays: string[] } | null;
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
 * `GET /api/specs/:name`. The metadata is always served; `source` only with a token, because hook
 * bodies are shell strings that routinely carry a credential inline. When it is held back the server
 * says so EXPLICITLY rather than sending an empty string — otherwise a page cannot tell "no access"
 * apart from "an empty spec".
 */
export type SpecDetail = SpecMeta & {
  source?: string;
  sourceWithheld?: true;
};
export type LogsResponse = { stack: string; tail: number; ok: boolean; text: string };
export type ActionResponse = { job: JobStub };
export type SubmitResponse = { id: string; kind: Kind; stack: string; specName?: string | null };

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
