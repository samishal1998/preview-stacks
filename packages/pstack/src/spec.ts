/**
 * The spec: parse + validate a `preview.yml` into a resolved Stack.
 *
 * Design notes that matter:
 *  - `${VAR}` interpolation happens ONCE, here, against a known env. Nothing downstream
 *    re-interpolates, so a value containing `${...}` can never be re-expanded by accident.
 *  - `stack` resolves to the single identity string every axis namespaces off. It is exported
 *    to hooks as $STACK so a hook never has to reconstruct it.
 *  - Axis order is significant: provisioned in declaration order, destroyed in REVERSE.
 *    Declare dependencies before dependents (database before the app that migrates it).
 */

import { SpecError } from './errors.ts';
import { parseSubdomains, resolveSubdomains, type SubdomainRoute } from './subdomains.ts';

export type Axis = {
  name: string;
  /** Provision. Must be idempotent — `up` is re-run on every redeploy. */
  up?: string;
  /** Destroy. Best-effort: a non-zero exit is reported but does not abort teardown. */
  down?: string;
  /** Exit 0 ⇒ the resource is GONE. Run by `verify` and after `down`. This is the leak gate. */
  assert_gone?: string;
  /** Exit 0 ⇒ the resource EXISTS. Run after `up` to fail fast on a silent provision failure. */
  assert_live?: string;
};

/**
 * What a deployment IS, which decides how it may be treated.
 *
 *   shared    A singleton the host provides and previews borrow — a database, a queue, a registry
 *             mirror. Created once, lives indefinitely. Has NO axes: there is nothing to isolate
 *             and nothing to prove gone. `down` is guarded, because `compose down -v` on one of
 *             these destroys state every other deployment depends on.
 *   isolated  One tenant — typically one PR. Created and destroyed constantly, HAS axes, and is
 *             expected to leave nothing behind.
 *
 * The kind is explicit rather than inferred from "does it have axes", because the guard on `down`
 * is load-bearing: a misconfigured isolated deployment that forgot its axes must not silently
 * inherit a singleton's protection.
 */
export type Kind = 'shared' | 'isolated';

/**
 * A precondition that must hold before `up`.
 *
 * An isolated deployment usually depends on shared ones — an ingress network, a reachable queue.
 * Without these, `up` fails partway through an axis hook with whatever error that CLI printed,
 * which tells you nothing. A requirement fails immediately, by name, before anything is created.
 */
export type Requirement = {
  name: string;
  /** Exit 0 ⇒ the precondition holds. */
  assert: string;
  /** Shown when it fails — say how to fix it, not just what broke. */
  hint?: string;
};

export type ComposeSpec = {
  file: string;
  /**
   * Profiles to bring up. Every service in a preview compose file should be behind a profile so a
   * bare `up` starts nothing. Teardown always passes ALL of these regardless of what was selected
   * — see compose.ts for why (it is the network-leak fix).
   */
  profiles: string[];
  /** Extra `-f` overlay files, applied in order after `file`. */
  overlays?: string[];
  /**
   * Wildcard subdomain routing per profile: anything under `<profile>-<stack>.<domain>` reaches the
   * same service the bare host does. Resolved into `PSTACK_WILD_<PROFILE>` env vars for your own
   * router labels to interpolate — see subdomains.ts, which also documents the DNS and TLS ceilings
   * (`depth: any` is routing-only; no certificate can cover it).
   */
  subdomains?: SubdomainRoute[];
};

export type Stack = {
  version: number;
  kind: Kind;
  /** Resolved stack identity, e.g. `pr-123`. Exported to every hook as $STACK. */
  stack: string;
  compose?: ComposeSpec;
  requires: Requirement[];
  axes: Axis[];
  /**
   * Fully-resolved env handed to compose and to every hook.
   *
   * DANGER — this is seeded from the WHOLE ambient environment before the spec's own `env:` is
   * layered on, so it contains every secret this process holds, `PSTACK_TOKEN` included. Never
   * serialise it, never log it, never return it from an HTTP response. Use `declaredEnv` to show a
   * spec's variables to a human.
   */
  env: Record<string, string>;
  /**
   * The variable names the SPEC ITSELF declared under `env:`, in declaration order.
   *
   * Exists so a UI can show "what this deployment configures" without dumping the ambient
   * environment. Values still need redacting before display — a spec author may legitimately write
   * `env: { SECRETS_TOKEN: \${SOME_TOKEN} }` — but the key set is authored, not inherited.
   */
  declaredEnv: string[];
};

// Declared in errors.ts and re-exported here so that `subdomains.ts` can throw it without an
// import cycle. Every existing `import { SpecError } from './spec.ts'` still resolves.
export { SpecError } from './errors.ts';

/**
 * The domain generated hostnames are anchored to.
 *
 * TWO NAMES, ONE THING. `PREVIEW_DOMAIN` is what every example, doc and the skill in this repo use,
 * and it is the name a spec should declare. `DOMAIN` is accepted because 0.3.0–0.7.0 read only that
 * (an oversight: it is the CONTROL stack's variable, written to `control/.env` to interpolate the
 * control compose file, and it was carried into the spec surface by mistake) and a spec written against
 * those versions must keep working.
 *
 * PRECEDENCE, most specific first:
 *
 *   1. `PREVIEW_DOMAIN` declared by the spec's own `env:`
 *   2. `DOMAIN` declared by the spec's own `env:`
 *   3. ambient `PREVIEW_DOMAIN`
 *   4. ambient `DOMAIN`
 *
 * A DECLARATION BEATS AN AMBIENT VALUE, which is the part that matters. `vars` is seeded from the whole
 * process environment before the spec's `env:` is layered on, so a stray `DOMAIN` exported in a shell
 * would otherwise anchor every generated rule — silently, on a wrong domain, producing a router that
 * deploys and never matches. `DOMAIN` is a generic enough name for that to happen by accident;
 * `PREVIEW_DOMAIN` is not, which is the other reason it is preferred.
 *
 * Both set and disagreeing is a warning rather than an error: it is a normal accident (an ambient
 * `DOMAIN` next to a declared `PREVIEW_DOMAIN`) and refusing to parse over it would be hostile.
 */
export function resolvePreviewDomain(
  vars: Record<string, string>,
  declaredEnv: readonly string[],
): string | undefined {
  const declared = (k: string) => (declaredEnv.includes(k) ? vars[k] : undefined);
  const chosen =
    declared('PREVIEW_DOMAIN') ?? declared('DOMAIN') ?? vars.PREVIEW_DOMAIN ?? vars.DOMAIN;

  if (vars.PREVIEW_DOMAIN && vars.DOMAIN && vars.PREVIEW_DOMAIN !== vars.DOMAIN) {
    warnings.push(
      `both PREVIEW_DOMAIN (${vars.PREVIEW_DOMAIN}) and DOMAIN (${vars.DOMAIN}) are set and differ; ` +
        `generated hostnames use "${chosen}". Declare PREVIEW_DOMAIN in the spec to be unambiguous.`,
    );
  }
  return chosen || undefined;
}

/** Non-fatal spec observations, surfaced by `pstack validate`. Reset per `loadSpec`. */
export const warnings: string[] = [];

/**
 * `${NAME}` from the request/deployment environment, or the EXPLICIT `${vars.NAME}` /
 * `${secrets.NAME}` from host-level storage. The namespace is deliberate, GitHub-style: a plain
 * `${REGION}` keeps meaning "whatever this request supplied", and a spec that reads host state says
 * so on its face — no precedence rule between the two can ever need explaining, because they are
 * spelled differently.
 */
const VAR = /\$\{(?:(vars|secrets)\.)?([A-Za-z_][A-Za-z0-9_]*)\}/g;

/** Host-level values, supplied by the control plane. Absent entirely under the bare CLI. */
export type HostValues = { vars: Record<string, string>; secrets: Record<string, string> };

/**
 * Substitute `${VAR}` from `vars`. Unknown variables are an error rather than an empty string:
 * silently expanding `${PR}` to "" yields a stack named `pr-` that collides across every PR, which
 * is far worse than failing loudly.
 */
export function interpolate(
  input: string,
  vars: Record<string, string>,
  where: string,
  host?: HostValues,
): string {
  const missing = new Set<string>();
  const out = input.replace(VAR, (_m, ns: string | undefined, name: string) => {
    if (ns) {
      if (!host) {
        // The bare CLI has no host store. Naming the boundary beats a generic "undefined
        // variable": the spec is fine, the runtime is the wrong one.
        throw new SpecError(
          `${where}: \${${ns}.${name}} — host ${ns} live on the control plane. Submit this spec ` +
            `to a pstack server, or use a plain \${${name}} and pass the value yourself.`,
        );
      }
      const v = host[ns as keyof HostValues][name];
      if (v === undefined || v === '') {
        missing.add(`${ns}.${name}`);
        return '';
      }
      return v;
    }
    const v = vars[name];
    if (v === undefined || v === '') {
      missing.add(name);
      return '';
    }
    return v;
  });
  if (missing.size > 0) {
    throw new SpecError(
      `${where}: undefined variable(s) ${[...missing].map((m) => `\${${m}}`).join(', ')}. ` +
        `Plain names are passed in the environment or under \`env:\`; \`vars.\` and \`secrets.\` ` +
        `names are defined on the host's Variables page.`,
    );
  }
  return out;
}

/**
 * True when an `assert_gone` is nothing but a negated probe, with no guard proving the probe could
 * have answered. Deliberately narrow: a script that already contains `exit`, `||` or `&&` is
 * assumed to handle its own failure modes, so guarded forms are not flagged.
 */
function isNaiveNegation(script: string): boolean {
  const lines = script
    .split('\n')
    .map((l) => l.replace(/#.*$/, '').trim())
    .filter(Boolean);
  if (lines.length !== 1) return false;
  const only = lines[0]!;
  if (!only.startsWith('!')) return false;
  return !/\bexit\b|\|\||&&/.test(only);
}

function asString(v: unknown, where: string): string {
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  throw new SpecError(`${where}: expected a string, got ${typeof v}`);
}

/**
 * Parse and fully resolve a spec.
 *
 * @param source  raw YAML
 * @param baseEnv the environment to interpolate against (usually process.env plus CLI overrides)
 */
export function parseSpec(
  source: string,
  baseEnv: Record<string, string | undefined>,
  host?: HostValues,
): Stack {
  // Reset here, not in loadSpec: warnings describe THIS parse. Accumulating across calls made a
  // long-lived server (which re-parses per request) report warnings from other stacks' specs.
  warnings.length = 0;

  let doc: unknown;
  try {
    doc = Bun.YAML.parse(source);
  } catch (err) {
    throw new SpecError(`invalid YAML: ${(err as Error).message}`);
  }
  if (doc === null || typeof doc !== 'object' || Array.isArray(doc)) {
    throw new SpecError('spec must be a YAML mapping');
  }
  const raw = doc as Record<string, unknown>;

  const version = Number(raw.version ?? 1);
  if (version !== 1) {
    throw new SpecError(`unsupported version ${version} (this build understands version 1)`);
  }

  const kindRaw = raw.kind === undefined ? 'isolated' : asString(raw.kind, 'kind');
  if (kindRaw !== 'shared' && kindRaw !== 'isolated') {
    throw new SpecError(`kind must be "shared" or "isolated", got "${kindRaw}"`);
  }
  const kind: Kind = kindRaw;

  // Start from the ambient environment, then layer the spec's own `env:` on top of it. Spec values
  // may themselves reference ambient vars, so they are interpolated as they are added — in
  // declaration order, letting a later entry build on an earlier one.
  const vars: Record<string, string> = {};
  for (const [k, v] of Object.entries(baseEnv)) if (v !== undefined) vars[k] = v;

  const declaredEnv: string[] = [];
  const specEnv = raw.env;
  if (specEnv !== undefined) {
    if (typeof specEnv !== 'object' || specEnv === null || Array.isArray(specEnv)) {
      throw new SpecError('`env` must be a mapping of NAME: value');
    }
    for (const [k, v] of Object.entries(specEnv as Record<string, unknown>)) {
      vars[k] = interpolate(asString(v, `env.${k}`), vars, `env.${k}`, host);
      declaredEnv.push(k);
    }
  }

  if (raw.stack === undefined) throw new SpecError('`stack` is required (the stack identity)');
  const rawStack = asString(raw.stack, 'stack');
  // The stack is IDENTITY: a compose project name, a hostname label, a value in every log line and
  // container name. A secret interpolated into it would be published by every surface redaction
  // cannot reach, so the reference itself is refused — not the resolved value scrubbed.
  if (/\$\{secrets\./.test(rawStack)) {
    throw new SpecError(
      'stack: must not reference ${secrets.*} — the stack name appears in container names, ' +
        'hostnames and logs, none of which can be redacted. Use ${vars.*} for identity parts.',
    );
  }
  const stack = interpolate(rawStack, vars, 'stack', host);
  if (!/^[a-z0-9][a-z0-9_-]*$/.test(stack)) {
    // Compose project names, DNS labels and most namespace APIs share roughly this alphabet.
    // Rejecting here beats a confusing failure five steps into a deploy.
    throw new SpecError(
      `stack "${stack}" must match /^[a-z0-9][a-z0-9_-]*$/ — it becomes a compose project name, ` +
        `a hostname label and (usually) a namespace identifier.`,
    );
  }
  vars.STACK = stack;

  let compose: ComposeSpec | undefined;
  if (raw.compose !== undefined) {
    const c = raw.compose;
    if (typeof c !== 'object' || c === null || Array.isArray(c)) {
      throw new SpecError('`compose` must be a mapping');
    }
    const cm = c as Record<string, unknown>;
    if (cm.file === undefined) throw new SpecError('`compose.file` is required when `compose` is set');
    const profiles = cm.profiles === undefined ? [] : cm.profiles;
    if (!Array.isArray(profiles)) throw new SpecError('`compose.profiles` must be a list');
    const overlays = cm.overlays === undefined ? [] : cm.overlays;
    if (!Array.isArray(overlays)) throw new SpecError('`compose.overlays` must be a list');
    const resolvedProfiles = profiles.map((p, i) =>
      interpolate(asString(p, `compose.profiles[${i}]`), vars, `compose.profiles[${i}]`, host),
    );
    compose = {
      file: interpolate(asString(cm.file, 'compose.file'), vars, 'compose.file', host),
      profiles: resolvedProfiles,
      overlays: overlays.map((o, i) =>
        interpolate(asString(o, `compose.overlays[${i}]`), vars, `compose.overlays[${i}]`, host),
      ),
    };
    // After the profiles, because a subdomain naming a profile that is never started is dead config
    // and worth refusing; and after `vars`, so `host:` can interpolate ${STACK} / ${DOMAIN}.
    const subConfigs = parseSubdomains(cm.subdomains, 'compose.subdomains', (v, at) =>
      interpolate(v, vars, at),
    );
    if (subConfigs.length > 0) {
      compose.subdomains = resolveSubdomains({
        configs: subConfigs,
        stack,
        profiles: resolvedProfiles,
        domain: resolvePreviewDomain(vars, declaredEnv),
      });
    }
  }

  const rawRequires = raw.requires === undefined ? [] : raw.requires;
  if (!Array.isArray(rawRequires)) throw new SpecError('`requires` must be a list');
  const requires: Requirement[] = rawRequires.map((r, i) => {
    if (typeof r !== 'object' || r === null || Array.isArray(r)) {
      throw new SpecError(`requires[${i}] must be a mapping`);
    }
    const rm = r as Record<string, unknown>;
    const name = asString(rm.name ?? '', `requires[${i}].name`);
    if (!name) throw new SpecError(`requires[${i}].name is required`);
    if (rm.assert === undefined) throw new SpecError(`requires[${i}].assert is required`);
    return {
      name,
      assert: interpolate(asString(rm.assert, `requires.${name}.assert`), vars, `requires.${name}.assert`, host),
      hint: rm.hint === undefined
        ? undefined
        : interpolate(asString(rm.hint, `requires.${name}.hint`), vars, `requires.${name}.hint`, host),
    };
  });

  const rawAxes = raw.axes === undefined ? [] : raw.axes;
  if (!Array.isArray(rawAxes)) throw new SpecError('`axes` must be a list');
  const seen = new Set<string>();
  const axes: Axis[] = rawAxes.map((a, i) => {
    if (typeof a !== 'object' || a === null || Array.isArray(a)) {
      throw new SpecError(`axes[${i}] must be a mapping`);
    }
    const am = a as Record<string, unknown>;
    const name = asString(am.name ?? '', `axes[${i}].name`);
    if (!name) throw new SpecError(`axes[${i}].name is required`);
    if (seen.has(name)) throw new SpecError(`duplicate axis name "${name}"`);
    seen.add(name);

    const field = (k: keyof Axis) =>
      am[k] === undefined
        ? undefined
        : interpolate(asString(am[k], `axes.${name}.${k}`), vars, `axes.${name}.${k}`, host);

    const axis: Axis = {
      name,
      up: field('up'),
      down: field('down'),
      assert_gone: field('assert_gone'),
      assert_live: field('assert_live'),
    };
    if (!axis.up && !axis.down && !axis.assert_gone && !axis.assert_live) {
      throw new SpecError(`axis "${name}" defines no up/down/assert_gone/assert_live — remove it`);
    }
    // An axis that can be created but never proven gone is exactly how leaks accumulate. Warn
    // rather than fail: some axes genuinely have no cheap existence probe.
    if (axis.up && !axis.assert_gone) {
      warnings.push(
        `axis "${name}" has \`up\` but no \`assert_gone\` — \`pstack verify\` cannot prove it was cleaned up.`,
      );
    }
    if (axis.assert_gone && isNaiveNegation(axis.assert_gone)) {
      // The #1 way an assert_gone lies. `! <probe>` exits 0 whenever <probe> fails for ANY
      // reason — missing CLI, expired token, unreachable API — so "cannot tell" is reported as
      // "gone", which is a false negative on the exact thing this tool exists to catch.
      warnings.push(
        `axis "${name}": \`assert_gone\` is a bare \`! <probe>\` with no reachability guard — it will ` +
          `report "gone" if the probe itself fails. Fail closed instead:\n` +
          `      <probe-is-usable> || exit 1\n` +
          `      ! <probe-for-this-resource>`,
      );
    }
    if (axis.assert_gone && /\|\|\s*true\b/.test(axis.assert_gone)) {
      // `|| true` forces exit 0, so the assert can never fail. It belongs on `down`, never here.
      warnings.push(
        `axis "${name}": \`assert_gone\` contains \`|| true\`, which makes it always pass. ` +
          `Tolerate failure in \`down\`, never in an assert.`,
      );
    }
    return axis;
  });

  // A singleton has nothing to isolate and nothing to prove gone, so axes on it are a category
  // error — almost always a spec that meant `kind: isolated`.
  if (kind === 'shared' && axes.length > 0) {
    throw new SpecError(
      `kind: shared cannot declare axes (found ${axes.length}). Axes exist to isolate one tenant ` +
        `from another and to prove a tenant's resources were cleaned up; a shared singleton has ` +
        `neither concern. Did you mean \`kind: isolated\`?`,
    );
  }
  if (kind === 'isolated' && axes.length === 0 && compose) {
    warnings.push(
      'kind: isolated with no axes — nothing per-tenant is provisioned or verified, so this is ' +
        'just a compose project. If it is a host singleton, mark it `kind: shared` so `down` is guarded.',
    );
  }

  return { version, kind, stack, compose, requires, axes, env: vars, declaredEnv };
}

/** Parse a spec file from disk. `parseSpec` resets `warnings`. */
export async function loadSpec(
  path: string,
  baseEnv: Record<string, string | undefined>,
  host?: HostValues,
): Promise<Stack> {
  const file = Bun.file(path);
  if (!(await file.exists())) throw new SpecError(`spec not found: ${path}`);
  return parseSpec(await file.text(), baseEnv, host);
}
