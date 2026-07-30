/**
 * Generate the Traefik labels and network wiring a preview service needs, from one short label.
 *
 * THE PROBLEM. A reachable preview service needs four Traefik labels and two network declarations,
 * every one of which has a silent failure mode — a missing `traefik.enable` makes the container
 * invisible, a non-`external` network makes it unreachable while looking correct, a router name
 * without the stack in it makes two PRs serve each other's containers. All six are boilerplate that
 * differs only by service name and port, and getting any one wrong produces a 404 with nothing logged.
 *
 * So declare the part that is actually yours:
 *
 *     services:
 *       app:
 *         image: nginx:alpine
 *         profiles: [app]
 *         labels:
 *           - pstack.routing.port=80
 *
 * and pstack writes the rest, correctly, including the TLS rule that inverts between challenge modes.
 *
 * ── IT NEVER OVERRIDES YOU ───────────────────────────────────────────────────────────────────────
 *
 * A service carrying ANY `traefik.*` label is left completely alone — labels, networks and all. That
 * is the escape hatch: the moment you need something this cannot express (a middleware chain, two
 * routers on one service, a non-standard hostname), write the labels yourself and pstack stops having
 * an opinion. There is no flag to remember and no merge to reason about; the presence of your own
 * label IS the opt-out.
 *
 * A service with neither a `pstack.routing.*` label nor a `traefik.*` one is also left alone — that is
 * a database or a worker, and forcing a hostname onto it would be wrong.
 *
 * ── WHY A DERIVED FILE AND NOT AN OVERLAY ────────────────────────────────────────────────────────
 *
 * The obvious implementation is a second `-f` overlay adding labels. It was rejected in 0.3.0 for the
 * same reason it is rejected here: Compose's merge semantics for list-form `labels` decide whether
 * YOUR routers survive the merge, and getting that wrong deletes them silently. Instead pstack reads
 * the submitted file, transforms it in memory, and writes a COMPLETE derived file that compose reads
 * on its own — no merge, nothing to reason about, and the result is inspectable on disk.
 *
 * The derived file is **JSON**, which is valid YAML 1.2 and what every YAML parser agrees on. Emitting
 * YAML would mean trusting a stringifier's quoting to match Go's parser on values like
 * ``Host(`app.example.com`)`` — a mismatch that would only show up on the user's host. Your original
 * file is never modified; this is written beside it.
 */

import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { Runner } from './exec.ts';
import { SpecError } from './errors.ts';
import type { Stack } from './spec.ts';
import { detectChallenge } from './inspect.ts';
import { wildcardRule } from './subdomains.ts';

/** The file pstack writes beside the submitted compose file. JSON, despite the extension — see above. */
export const GENERATED_COMPOSE = 'compose.generated.yml';

const INGRESS = 'preview-ingress';
const SHARED = 'preview-shared';

/** What a service asked for, read from its `pstack.routing.*` labels. */
export type RoutingRequest = {
  /** The port INSIDE the container. Traefik dials the container's ingress IP on this. */
  port: number;
  /**
   * The name used for the hostname and for the router/service ids. Defaults to the compose service
   * name, which is what `pstack.routing.service_name` overrides.
   */
  name: string;
  /** A complete hostname, when the `<name>-<stack>.<domain>` convention does not fit. */
  host?: string;
};

export type AugmentResult = {
  /** The transformed document, ready to serialise. */
  doc: Record<string, unknown>;
  /** Service name → the labels pstack added, for reporting. */
  generated: Record<string, string[]>;
  /** Service name → why it was left alone. */
  skipped: Record<string, string>;
};

/** Labels in a compose service are either a list of `k=v` or a mapping. Normalise to a mapping. */
export function labelsToMap(labels: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (Array.isArray(labels)) {
    for (const entry of labels) {
      if (typeof entry !== 'string') continue;
      const eq = entry.indexOf('=');
      if (eq === -1) out[entry] = '';
      else out[entry.slice(0, eq)] = entry.slice(eq + 1);
    }
  } else if (labels && typeof labels === 'object') {
    for (const [k, v] of Object.entries(labels as Record<string, unknown>)) out[k] = String(v ?? '');
  }
  return out;
}

/**
 * Read a service's routing request.
 *
 * Returns null when it asked for nothing. Throws on a port that is not a port: a service that
 * declares `pstack.routing.port=web` wanted routing and will not get it, and silently skipping is how
 * that becomes a 404 nobody can explain.
 */
export function readRoutingRequest(
  service: string,
  labels: Record<string, string>,
): RoutingRequest | null {
  const portRaw = labels['pstack.routing.port'];
  const name = labels['pstack.routing.service_name'] || service;
  const host = labels['pstack.routing.host'] || undefined;
  if (portRaw === undefined) {
    // A name or host without a port cannot produce a working router, and asking for one implies the
    // rest was meant to be there.
    if (labels['pstack.routing.service_name'] || labels['pstack.routing.host']) {
      throw new SpecError(
        `service "${service}" sets pstack.routing.* but no pstack.routing.port, so no router can be ` +
          `generated — Traefik needs the port inside the container.`,
      );
    }
    return null;
  }
  const port = Number(portRaw);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new SpecError(
      `service "${service}" has pstack.routing.port="${portRaw}", which is not a port number. ` +
        `Use the port INSIDE the container, e.g. 80.`,
    );
  }
  return { port, name, host };
}

/**
 * Add the labels and networks a routed service needs.
 *
 * `challenge` decides one line and inverts between modes: under HTTP-01 every router resolves its own
 * certificate and so needs `tls.certresolver`; under DNS-01 one always-on router holds the wildcard and
 * a per-router certresolver makes each PR order its own, burning the weekly rate limit. Getting this
 * from the running Traefik (see `detectChallenge`) rather than a setting means the generated labels
 * cannot disagree with the host they are deployed to.
 */
export function augmentComposeDoc(args: {
  doc: Record<string, unknown>;
  spec: Stack;
  challenge: 'http01' | 'dns01' | 'unknown';
  domain?: string;
}): AugmentResult {
  const { spec, challenge } = args;
  // Structurally clone so a caller's document is never mutated — this runs on every compose command.
  const doc = JSON.parse(JSON.stringify(args.doc)) as Record<string, unknown>;
  const generated: Record<string, string[]> = {};
  const skipped: Record<string, string> = {};

  const services = (doc.services ?? {}) as Record<string, Record<string, unknown>>;
  if (typeof services !== 'object' || Array.isArray(services)) {
    throw new SpecError('`services` in the compose file must be a mapping');
  }

  let anyRouted = false;

  for (const [name, svcRaw] of Object.entries(services)) {
    if (!svcRaw || typeof svcRaw !== 'object') continue;
    const svc = svcRaw as Record<string, unknown>;
    const labels = labelsToMap(svc.labels);

    // Your labels win, entirely. Not merged, not partially applied.
    if (Object.keys(labels).some((k) => k.startsWith('traefik.'))) {
      skipped[name] = 'has its own traefik.* labels';
      continue;
    }

    const req = readRoutingRequest(name, labels);
    if (!req) {
      skipped[name] = 'no pstack.routing.port — not routed (a database or worker needs no hostname)';
      continue;
    }

    const domain = args.domain ?? spec.env.DOMAIN;
    if (!req.host && !domain) {
      throw new SpecError(
        `service "${name}" asks for routing but there is no DOMAIN to build a hostname from. ` +
          `Declare it in the spec (env:\\n  DOMAIN: preview.example.com) or set ` +
          `pstack.routing.host on the service.`,
      );
    }

    // The stack goes in the router and service ids because Traefik's namespace is GLOBAL across the
    // daemon: two deployments sharing a router name overwrite each other, and one hostname starts
    // serving the other's container.
    const id = `${req.name}-${spec.stack}`;
    const host = req.host ?? `${req.name}-${spec.stack}.${domain}`;

    const added = [
      // The host runs `--providers.docker.exposedbydefault=false`, so without this the container is
      // invisible to Traefik and the hostname 404s with nothing logged anywhere.
      'traefik.enable=true',
      // This container is on more than one network; Traefik must be told which to dial.
      `traefik.docker.network=${INGRESS}`,
      `traefik.http.routers.${id}.rule=Host(\`${host}\`)`,
      `traefik.http.routers.${id}.entrypoints=websecure`,
      `traefik.http.routers.${id}.tls=true`,
      `traefik.http.routers.${id}.service=${id}`,
      // The port INSIDE the container. Traefik resolves the container's ingress IP and dials this on
      // it — no host-port publishing is involved.
      `traefik.http.services.${id}.loadbalancer.server.port=${req.port}`,
    ];
    if (challenge !== 'dns01') {
      // Under HTTP-01 each hostname resolves its own certificate. Included when the mode is unknown
      // too: a missing certresolver on an HTTP-01 host means no certificate at all, while a spare one
      // on DNS-01 costs a redundant certificate — the cheaper mistake.
      added.push(`traefik.http.routers.${id}.tls.certresolver=le`);
    }

    // A wildcard subdomain router, when the spec asked for one for this profile. Same service, lower
    // priority, so any exact host — including the one just generated — still wins.
    const sub = spec.compose?.subdomains?.find((s) => s.profile === req.name);
    if (sub) {
      added.push(
        `traefik.http.routers.${id}-wild.rule=${wildcardRule(sub.host, sub.depth)}`,
        `traefik.http.routers.${id}-wild.priority=2`,
        `traefik.http.routers.${id}-wild.entrypoints=websecure`,
        `traefik.http.routers.${id}-wild.tls=true`,
        `traefik.http.routers.${id}-wild.service=${id}`,
      );
      if (challenge !== 'dns01') added.push(`traefik.http.routers.${id}-wild.tls.certresolver=le`);
    }

    // Keep whatever the user wrote (the pstack.routing.* labels included, so the file stays
    // self-describing) and append. Always a list: mixing forms in one file is legal but confusing.
    svc.labels = [...Object.entries(labels).map(([k, v]) => (v === '' ? k : `${k}=${v}`)), ...added];

    // Networks: append, never replace. A service already on its own networks keeps them.
    const existing = Array.isArray(svc.networks)
      ? (svc.networks as unknown[]).filter((n): n is string => typeof n === 'string')
      : svc.networks && typeof svc.networks === 'object'
        ? Object.keys(svc.networks as Record<string, unknown>)
        : [];
    const wanted = [...new Set([...(existing.length ? existing : ['default']), INGRESS])];
    svc.networks = wanted;

    generated[name] = added;
    anyRouted = true;
  }

  // Root networks. Only the ones that are missing, so a user's own definition is never overwritten —
  // and `external: true` is the whole point: declared without it, compose creates
  // `<project>_preview-ingress` and the container is up, healthy and unreachable.
  if (anyRouted) {
    const networks = (doc.networks ?? {}) as Record<string, unknown>;
    if (!('default' in networks)) networks.default = {};
    if (!(INGRESS in networks)) networks[INGRESS] = { external: true };
    // Declared even when nothing uses it: every per-PR file in the wild references it, and an absent
    // external network is a hard compose error rather than a quiet one.
    if (!(SHARED in networks)) networks[SHARED] = { external: true };
    doc.networks = networks;
  }

  return { doc, generated, skipped };
}

/**
 * Read the submitted compose file, augment it, and write the derived file next to it.
 *
 * Returns the filename compose should use — the derived one when anything was generated, and the
 * original otherwise, so a deployment that writes its own labels gets exactly the file it submitted
 * with no derived artefact left lying around.
 *
 * Regenerated on every compose invocation rather than cached at submit time, because the labels depend
 * on the RESOLVED spec: `up` with one set of variables and `down` with another must not disagree about
 * what the router was called.
 */
export async function materializeCompose(args: {
  dir: string;
  spec: Stack;
  runner: Runner;
  /** Skip the docker probe when the caller already knows (tests, or a batch). */
  challenge?: 'http01' | 'dns01' | 'unknown';
}): Promise<{ file: string; generated: Record<string, string[]>; skipped: Record<string, string> }> {
  const { dir, spec, runner } = args;
  const original = spec.compose!.file;
  const source = join(dir, original);

  let raw: string;
  try {
    raw = await readFile(source, 'utf8');
  } catch {
    // Not an error here: the file may legitimately live somewhere this process cannot read, and
    // compose will report a missing file far better than a guess would.
    return { file: original, generated: {}, skipped: {} };
  }

  let doc: unknown;
  try {
    doc = Bun.YAML.parse(raw);
  } catch (err) {
    throw new SpecError(`compose file ${original} is not valid YAML: ${(err as Error).message}`);
  }
  if (!doc || typeof doc !== 'object' || Array.isArray(doc)) {
    throw new SpecError(`compose file ${original} must be a mapping`);
  }

  // Cheap pre-check: no pstack.routing.* anywhere means nothing to do, and no reason to shell out to
  // docker for the challenge mode.
  if (!raw.includes('pstack.routing.')) {
    return { file: original, generated: {}, skipped: {} };
  }

  const challenge = args.challenge ?? (await detectChallenge(runner));
  const result = augmentComposeDoc({ doc: doc as Record<string, unknown>, spec, challenge });
  if (Object.keys(result.generated).length === 0) {
    return { file: original, generated: {}, skipped: result.skipped };
  }

  // JSON, which every YAML parser reads identically. Written beside the original, never over it.
  await writeFile(join(dir, GENERATED_COMPOSE), `${JSON.stringify(result.doc, null, 2)}\n`, 'utf8');
  return { file: GENERATED_COMPOSE, generated: result.generated, skipped: result.skipped };
}
