/**
 * Library entry point.
 *
 * The CLI (`pstack`) is the primary interface, but the core is importable so you can embed the
 * lifecycle in your own tooling — a bespoke deploy script, a bot, a different HTTP surface.
 *
 *   import { loadSpec, createRunner, up, down, verify } from '@samyx/preview-stacks';
 *
 *   const spec   = await loadSpec('preview.yml', { ...process.env, PR: '123' });
 *   const runner = createRunner({ dryRun: false });
 *   const result = await down(spec, runner);          // tears down, then verifies
 *   if (!result.ok) throw new Error('something leaked');
 *
 * Pass a `Sink` as the last argument to capture progress instead of writing to stdout (see
 * `bufferSink`) — that is how the HTTP API streams a job's log.
 */

export { loadSpec, parseSpec, interpolate, warnings, SpecError } from './spec.ts';
export type { Axis, ComposeSpec, Orchestrator, SleepPolicy, Stack } from './spec.ts';
export { parseDuration } from './spec.ts';
export { swarmify, swarmInfo, workerJoinToken, joinCommand, joinScript, SWARM_PORTS } from './swarm.ts';
export type { SwarmInfo, SwarmNode, SwarmifyResult } from './swarm.ts';
export { signShare, verifyShare, SHARE_VIEWS } from './share.ts';
export type { ShareClaims, ShareView } from './share.ts';

export { createRunner, captureOutputs, maskSecrets } from './exec.ts';
export type { Runner, RunResult, LogLevel } from './exec.ts';

export { consoleSink, bufferSink, nullSink } from './log.ts';
export type { Sink, LogEvent } from './log.ts';

export { up, down, sleep, verify, status, report } from './stack.ts';
export type { Outcome, StepResult } from './stack.ts';

export { composeUp, composeDown, composePs, composeLogs, shq } from './compose.ts';

export {
  escapeHostRegexp,
  parseSubdomains,
  resolveSubdomains,
  subdomainEnv,
  subdomainVarName,
  wildcardRule,
} from './subdomains.ts';
export type { SubdomainConfig, SubdomainDepth, SubdomainRoute } from './subdomains.ts';

export { EventBus, events, EVENTS, WILDCARD, isEventName, isSubscribable } from './events.ts';
export type { EventName, PstackEvent, Listener } from './events.ts';
export { Webhooks, WebhookError } from './webhooks.ts';
export type { NotifierRow, DeliveryRow } from './webhooks.ts';
export {
  Dispatcher,
  NotifierError,
  TYPES,
  assertDeliverableUrl,
  validateConfig,
  webhookType,
} from './notify.ts';
export type { DeliveryResult, NotifierField, NotifierType } from './notify.ts';

export { readControlState, planUpgrade, planUiSwitch, switchUi, upgrade, UpgradeError } from './upgrade.ts';
export type { ControlState, UpgradeStep } from './upgrade.ts';
export { Store } from './store.ts';
export type { UserRow } from './store.ts';
export { Auth, AuthError } from './auth.ts';
export type { Principal } from './auth.ts';

export {
  RegistryAuthStore,
  RegistryAuthError,
  normalizeRegistry,
  decodeUsername,
  DOCKER_HUB_KEY,
} from './registries.ts';
export type { RegistryEntry, RegistryState } from './registries.ts';

export {
  augmentComposeDoc,
  labelsToMap,
  materializeCompose,
  readRoutingRequest,
  GENERATED_COMPOSE,
} from './autolabel.ts';
export type { AugmentResult, RoutingRequest } from './autolabel.ts';

export {
  allTraefikRouters,
  deploymentRuntime,
  detectChallenge,
  hostsFromRule,
  routesFromLabels,
} from './inspect.ts';
export type { ContainerInfo, Finding, PortMap, RouteInfo, Runtime } from './inspect.ts';

export { RoutingStore, RoutingError, assertValidRoutingName, validateRoutingContent } from './routing.ts';
export type { RoutingFile } from './routing.ts';

export { SpecStore, SpecStoreError, assertValidSpecName, findRequiredVars } from './specs.ts';
export type { SpecMeta, StoredSpec } from './specs.ts';

export { JobRegistry } from './jobs.ts';
export type { Job, JobAction, JobState } from './jobs.ts';

export { displayVar, displayDeclared, isSecretName, mask, redactText } from './redact.ts';
export type { DisplayVar, Visibility } from './redact.ts';

export { buildImage, DEFAULT_IMAGE_TAG } from './image.ts';
export type { BuildImageOptions } from './image.ts';

export { init, CONTROL_PROJECT } from './init.ts';
export type { InitOptions } from './init.ts';

export { createServer } from './api.ts';
export type { ServerOptions } from './api.ts';
