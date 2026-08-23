/**
 * `pstack upgrade` — move a host to a new version of pstack.
 *
 * ── WHY A COMMAND AND NOT A PARAGRAPH IN THE DOCS ────────────────────────────────────────────────
 *
 * The upgrade was always three commands (install, rebuild the image, re-run `init`), and the third
 * one has a trap: `init` takes `PSTACK_TOKEN` from the environment and MINTS A FRESH ONE when it is
 * absent. So the obvious `pstack init --domain … --acme-email …` silently rotates the machine token
 * and every CI job starts getting 401s — from a command whose purpose was "change nothing but the
 * version". Everything needed to avoid that is already on disk in `control/.env`; nothing but habit
 * was stopping the CLI from reading it.
 *
 * ── THE RE-EXEC IS LOAD-BEARING ──────────────────────────────────────────────────────────────────
 *
 * `pstack build-image` writes a Dockerfile that installs `@samyx/preview-stacks@<the running CLI's
 * own version>` — that pin is what stops the image and the CLI disagreeing. Which means a single
 * process CANNOT do the whole upgrade: after installing 0.26.0, this process is still the 0.25.x
 * code that was loaded at start, so its `build-image` would faithfully build a 0.25.x image and
 * report success. The observable result is an "upgrade" that changes the CLI and leaves the running
 * server exactly where it was.
 *
 * So it is two phases. Phase 1 installs and then executes the NEWLY INSTALLED binary with
 * `--resume`; phase 2 (running as the new version) rebuilds and re-inits. The handoff is a real
 * process boundary, which is also why the second half is a plain `pstack upgrade --resume` an
 * operator can run by hand if the first half already succeeded.
 *
 * ── WHAT IT DELIBERATELY DOES NOT DO ─────────────────────────────────────────────────────────────
 *
 * It does not run from the API. The control plane is a container inside the stack being recreated;
 * upgrading itself means killing the process mid-request, and a broken new image would leave the
 * host with no control plane and no remote way to repair it. This is CLI-only for the same reason
 * `init` is — see the header of init.ts.
 */

import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { shq } from './compose.ts';
import type { Runner } from './exec.ts';
import pkg from '../package.json' with { type: 'json' };

export class UpgradeError extends Error {}

/** Everything `init` needs, recovered from what a previous `init` left on disk. */
export type ControlState = {
  /** The existing machine token. Reused, never regenerated — the whole point of this command. */
  token: string;
  domain: string;
  acmeEmail: string;
  dnsProvider: string;
  /**
   * Not in `.env`: the challenge is baked into the GENERATED compose file as Traefik flags, so it
   * is read back from there. Getting this wrong is expensive in a way a re-run cannot undo — under
   * HTTP-01 each router resolves its own certificate, under DNS-01 one wildcard covers them, and
   * flipping a live host to the wrong one burns the weekly rate limit.
   */
  challenge: 'http01' | 'dns01';
  /** `advanced` when the control stack has the separate UI container, so it gets rebuilt too. */
  ui: 'basic' | 'advanced';
  /**
   * `swarm` or `compose`, from `.env`. A host provisioned before 0.26.0 has no such line and IS a
   * compose host — so absent means compose, never the new-host default. Switching is explicit
   * (`--orchestrator`) because it recreates the networks, which needs every preview down.
   */
  orchestrator: 'swarm' | 'compose';
};

/**
 * Read what the running control stack was configured with.
 *
 * Parsed rather than re-asked, because every value here is one an operator would have to remember
 * exactly — and a wrong `--domain` on an upgrade rewrites every router rule on the host.
 */
export async function readControlState(dataDir: string): Promise<ControlState> {
  const controlDir = join(dataDir, 'control');
  const envPath = join(controlDir, '.env');
  let envText: string;
  try {
    envText = await readFile(envPath, 'utf8');
  } catch {
    throw new UpgradeError(
      `no control stack found at ${controlDir} (${envPath} is missing). This host has never run ` +
        `\`pstack init\`, or its data directory is elsewhere — set PSTACK_DATA to point at it.`,
    );
  }

  const env = new Map<string, string>();
  for (const line of envText.split('\n')) {
    const m = /^([A-Z_][A-Z0-9_]*)=(.*)$/.exec(line.trim());
    if (m) env.set(m[1]!, m[2]!);
  }
  const need = (key: string): string => {
    const v = env.get(key);
    if (!v) {
      throw new UpgradeError(
        `${envPath} has no ${key}. It was written by an older \`pstack init\` or edited by hand; ` +
          `re-run \`pstack init\` with the flags you want and this file will be complete again.`,
      );
    }
    return v;
  };

  // The compose file is generated, so reading it back is reading `init`'s own decision rather than
  // guessing. Absent (a half-finished install) is a refusal, not a default: defaulting to http01 on
  // a dns01 host would make every per-PR router order its own certificate.
  const composePath = join(controlDir, 'docker-compose.yml');
  let compose: string;
  try {
    compose = await readFile(composePath, 'utf8');
  } catch {
    throw new UpgradeError(
      `${composePath} is missing, so the ACME challenge mode cannot be read back. Re-run ` +
        `\`pstack init\` with the flags this host needs instead of upgrading blind.`,
    );
  }

  return {
    token: need('PSTACK_TOKEN'),
    domain: need('DOMAIN'),
    acmeEmail: need('ACME_EMAIL'),
    dnsProvider: env.get('DNS_PROVIDER') ?? '',
    challenge: /dnschallenge/i.test(compose) ? 'dns01' : 'http01',
    /*
     * The service init injects is `advanced-ui`. It used to look for `pstack-ui:` — which appears in
     * every generated file as a Traefik ROUTER name (`traefik.http.routers.pstack-ui.rule=…`), never
     * as a service key — so the anchored pattern matched nothing, every host read as `basic`, and an
     * upgrade silently removed the advanced UI it was supposed to preserve.
     *
     * The fixture that "passed" was hand-written with the name I assumed. The test now generates a
     * real control directory with `init` and reads THAT, which is the only version of this check that
     * could have failed honestly.
     */
    ui: /^\s{2}advanced-ui:/m.test(compose) ? 'advanced' : 'basic',
    orchestrator: env.get('PSTACK_ORCHESTRATOR') === 'swarm' ? 'swarm' : 'compose',
  };
}

/**
 * Where a global `bun install -g` must put the binary for THIS install to be the one replaced.
 *
 * The cloud-config installs with `BUN_INSTALL=/usr/local`, so the binary is `/usr/local/bin/pstack`.
 * A plain `bun install -g` on that host writes to `~/.bun` instead and leaves `/usr/local/bin/pstack`
 * at the old version — an upgrade that reports success and changes nothing an operator would notice
 * until the next deploy behaves like the old code. So the prefix is derived from where the running
 * binary actually is, not from the environment's default.
 */
export function installPrefixFor(binPath: string | null): string | null {
  if (!binPath) return null;
  const bin = dirname(binPath);
  // `<prefix>/bin/pstack` → `<prefix>`. Anything else (a linked checkout, a shim somewhere odd) is
  // reported as unknown rather than guessed at.
  return bin.endsWith('/bin') ? dirname(bin) : null;
}

export type UpgradeStep = { label: string; cmd: string; env?: Record<string, string> };

/**
 * The `init` flags that reproduce a host's current configuration.
 *
 * One builder for both the upgrade and the UI switch: two copies would drift, and the failure mode of
 * a drifted copy here is a re-init that quietly changes something nobody asked to change.
 */
function initFlags(state: ControlState): string {
  return [
    `--domain ${shq(state.domain)}`,
    `--acme-email ${shq(state.acmeEmail)}`,
    `--challenge ${state.challenge}`,
    ...(state.challenge === 'dns01' && state.dnsProvider ? [`--dns-provider ${shq(state.dnsProvider)}`] : []),
    ...(state.ui === 'advanced' ? ['--ui advanced'] : []),
    `--orchestrator ${state.orchestrator}`,
  ].join(' ');
}

/**
 * The commands, in order, without running any of them.
 *
 * Split out so a `--dry-run` prints exactly what a real run would execute, and so the sequence is
 * testable on a machine with neither docker nor a control stack.
 */
export function planUpgrade(args: {
  phase: 'install' | 'resume';
  target: string;
  state: ControlState;
  /** Resolved path of the `pstack` binary, for the re-exec and the install prefix. */
  binPath: string | null;
}): UpgradeStep[] {
  const { phase, target, state } = args;
  if (phase === 'install') {
    const prefix = installPrefixFor(args.binPath);
    return [
      {
        label: `install @samyx/preview-stacks@${target}`,
        // `bun` rather than an absolute path: it is next to `pstack` on every host this ships to,
        // and hardcoding /usr/local/bin/bun would break a user-prefix install.
        cmd: `bun install -g @samyx/preview-stacks@${shq(target)}`,
        // Same prefix the running binary lives under, or bun's default when it cannot be derived.
        ...(prefix ? { env: { BUN_INSTALL: prefix } } : {}),
      },
      {
        // The handoff. `pstack` from PATH is now the NEW version — see the file header for why this
        // cannot be a function call.
        label: 'rebuild and re-init as the new version',
        cmd: `pstack upgrade --resume`,
      },
    ];
  }

  return [
    { label: 'build the control image', cmd: 'pstack build-image' },
    ...(state.ui === 'advanced'
      ? [{ label: 'build the advanced UI image', cmd: 'pstack build-image --ui' }]
      : []),
    {
      label: 're-run init (same domain, same token)',
      cmd: `pstack init ${initFlags(state)}`,
      // THE line this command exists for. Without it `init` generates a new token and every CI job
      // holding the old one starts getting 401s.
      env: { PSTACK_TOKEN: state.token },
    },
  ];
}

/**
 * The steps for changing ONLY the UI mode.
 *
 * Not `planUpgrade`'s resume list: that rebuilds the control image, which takes minutes and has
 * nothing to do with which UI is served. Switching to `advanced` builds the SPA image (init refuses
 * without it, by name); switching back to `basic` builds nothing, because the API already carries
 * the embedded UI.
 */
export function planUiSwitch(state: ControlState): UpgradeStep[] {
  return [
    ...(state.ui === 'advanced'
      ? [{ label: 'build the advanced UI image', cmd: 'pstack build-image --ui' }]
      : []),
    {
      label: `re-run init with the ${state.ui} UI`,
      cmd: `pstack init ${initFlags(state)}`,
      // Same reason as an upgrade: without it `init` mints a new token and every CI job breaks.
      env: { PSTACK_TOKEN: state.token },
    },
  ];
}

/**
 * Switch a host between the embedded UI and the standalone SPA container.
 *
 * Everything needed is already on disk, which is the whole point — the alternative is re-typing
 * `init`'s full command line with the token, and getting the token wrong is how a working host stops
 * answering CI.
 */
export async function switchUi(opts: {
  dataDir: string;
  ui: 'basic' | 'advanced';
  runner: Runner;
  log?: (line: string) => void;
}): Promise<{ changed: boolean; steps: UpgradeStep[] }> {
  const say = opts.log ?? ((l: string) => console.log(l));
  const current = await readControlState(opts.dataDir);
  if (current.ui === opts.ui) {
    // No container recreation for a no-op: recreating the control stack is a brief outage, and
    // "already advanced" is an answer, not a reason to cause one.
    say(`Already serving the ${opts.ui} UI. Nothing to do.`);
    return { changed: false, steps: [] };
  }

  const state: ControlState = { ...current, ui: opts.ui };
  const steps = planUiSwitch(state);
  say(`${current.ui} UI → ${opts.ui} UI   (${state.domain})`);
  for (const step of steps) {
    say(`  → ${step.label}`);
    const r = await opts.runner.run(step.cmd, {
      env: { ...process.env, ...step.env } as Record<string, string>,
      label: step.label,
    });
    if (!r.ok && !r.skipped) {
      throw new UpgradeError(
        `${step.label} failed (exit ${r.code}).\n` +
          `${(r.stderr || r.stdout).trim().split('\n').slice(-8).join('\n')}`,
      );
    }
    if (r.stdout.trim()) say(indent(r.stdout));
  }
  return { changed: true, steps };
}

/**
 * Run it.
 *
 * Stops at the first failing step and says which — a half-applied upgrade is recoverable by hand
 * (the steps are ordinary commands), and continuing past a failed image build would recreate the
 * stack from an image that does not exist.
 */
export async function upgrade(opts: {
  dataDir: string;
  /** npm dist-tag or exact version. `latest` is resolved by bun, not by us. */
  target?: string;
  phase?: 'install' | 'resume';
  /**
   * Force the UI mode instead of reading it back.
   *
   * Exists because detection was wrong once: a host whose advanced UI was dropped by a bad upgrade
   * now reads as `basic` — truthfully, since that IS its current state — so putting it back needs a
   * way to say so without hand-writing the whole `init` line and its token.
   */
  ui?: 'basic' | 'advanced';
  /** Switch the host's orchestrator. Only when typed; init refuses while previews hold the networks. */
  orchestrator?: 'swarm' | 'compose';
  runner: Runner;
  log?: (line: string) => void;
}): Promise<{ from: string; to: string; steps: UpgradeStep[] }> {
  const say = opts.log ?? ((l: string) => console.log(l));
  const phase = opts.phase ?? 'install';
  const target = opts.target ?? 'latest';
  const detected = await readControlState(opts.dataDir);
  const state: ControlState = {
    ...detected,
    ...(opts.ui ? { ui: opts.ui } : {}),
    ...(opts.orchestrator ? { orchestrator: opts.orchestrator } : {}),
  };

  // `Bun.which` resolves the same PATH the re-exec below will use, so the prefix and the handoff
  // can never disagree about which `pstack` is being talked about.
  const binPath = Bun.which('pstack');
  const steps = planUpgrade({ phase, target, state, binPath });

  if (phase === 'install') {
    say(
      `pstack ${pkg.version} → ${target}   (${state.domain}, ${state.challenge}, ${state.ui} UI, ${state.orchestrator}` +
        `${opts.ui && opts.ui !== detected.ui ? ` — overriding the detected ${detected.ui}` : ''}` +
        `${opts.orchestrator && opts.orchestrator !== detected.orchestrator ? ` — switching from ${detected.orchestrator}` : ''})`,
    );
    if (!binPath) {
      say('  note: `pstack` is not on PATH, so the global install prefix could not be derived.');
      say('        bun will use its own default, which may not be where this binary lives.');
    }
  } else {
    say(`pstack ${pkg.version} — rebuilding and recreating the control stack`);
  }

  for (const step of steps) {
    say(`  → ${step.label}`);
    const r = await opts.runner.run(step.cmd, { env: { ...process.env, ...step.env } as Record<string, string>, label: step.label });
    if (!r.ok && !r.skipped) {
      throw new UpgradeError(
        `${step.label} failed (exit ${r.code}).\n` +
          `${(r.stderr || r.stdout).trim().split('\n').slice(-8).join('\n')}\n\n` +
          `Nothing after this step ran. The remaining steps are ordinary commands you can run by ` +
          `hand:\n${steps.slice(steps.indexOf(step) + 1).map((s) => `  ${s.cmd}`).join('\n') || '  (none)'}`,
      );
    }
    // The install step's own output is noise; the re-exec's is the upgrade's real transcript.
    if (step.cmd.startsWith('pstack ') && r.stdout.trim()) say(indent(r.stdout));
  }

  /*
   * A dry run must show the WHOLE upgrade, not half of it. Phase 2 runs in a process that a dry run
   * never spawns, so its commands would otherwise be invisible — and those are the interesting ones:
   * they carry the domain, the challenge mode and the token passthrough an operator wants to check
   * before letting this touch a live host.
   */
  if (opts.runner.dryRun && phase === 'install') {
    say('  then, as the newly installed version:');
    for (const step of planUpgrade({ phase: 'resume', target, state, binPath })) {
      const env = step.env?.PSTACK_TOKEN ? ' (with the existing PSTACK_TOKEN)' : '';
      say(`    ${step.cmd}${env}`);
    }
  }

  return { from: pkg.version, to: target, steps };
}

function indent(s: string): string {
  return s.trimEnd().split('\n').map((l) => `    | ${l}`).join('\n');
}
