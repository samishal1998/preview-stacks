#!/usr/bin/env bun
/**
 * pstack — declarative lifecycle for ephemeral preview stacks.
 *
 *   pstack up        provision every axis, then compose up
 *   pstack down      compose down (all profiles), destroy axes in reverse, then verify
 *   pstack verify    assert every axis is gone; non-zero exit if anything leaked
 *   pstack status    what is running for this stack
 *   pstack validate  parse the spec, resolve interpolation, report warnings
 *   pstack init      stand up the CONTROL stack on this host (traefik + pstack api/ui)
 *   pstack serve     HTTP API + web UI over the deployment registry
 *
 * `init` runs from the HOST and is the only thing that manages the control stack; `serve`
 * manages every OTHER deployment. See docs/control-plane.md for why that split exists.
 *
 * Global flags:  -f/--file <preview.yml>  --dry-run  -v/--verbose  -q/--quiet
 *                --set KEY=VALUE (repeatable; overrides the environment)
 *                --no-verify  (down only)
 *
 * Exit codes:  0 ok · 1 operation failed · 2 leaked resources (verify) · 3 bad spec/usage
 * The distinct code for leaks lets CI treat "torn down but leaked" differently from "teardown
 * errored", which are different problems with different owners.
 */

import { loadSpec, SpecError, warnings } from './spec.ts';
import { createRunner, type LogLevel } from './exec.ts';
import { down, report, status, up, verify } from './stack.ts';

const EXIT = { ok: 0, failed: 1, leaked: 2, usage: 3 } as const;

type Parsed = {
  cmd: string;
  file: string;
  dryRun: boolean;
  level: LogLevel;
  overrides: Record<string, string>;
  noVerify: boolean;
  force: boolean;
  domain: string;
  acmeEmail: string;
  dnsProvider: string;
};

function parseArgs(argv: string[]): Parsed {
  const p: Parsed = {
    cmd: '',
    file: 'preview.yml',
    dryRun: false,
    level: 'normal',
    overrides: {},
    noVerify: false,
    force: false,
    domain: process.env.PSTACK_DOMAIN ?? '',
    acmeEmail: process.env.PSTACK_ACME_EMAIL ?? '',
    dnsProvider: process.env.PSTACK_DNS_PROVIDER ?? '',
  };
  const rest: string[] = [];
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]!;
    if (a === '-f' || a === '--file') p.file = argv[++i] ?? p.file;
    else if (a === '--dry-run' || a === '-n') p.dryRun = true;
    else if (a === '-v' || a === '--verbose') p.level = 'verbose';
    else if (a === '-q' || a === '--quiet') p.level = 'quiet';
    else if (a === '--no-verify') p.noVerify = true;
    else if (a === '--force') p.force = true;
    else if (a === '--domain') p.domain = argv[++i] ?? p.domain;
    else if (a === '--acme-email') p.acmeEmail = argv[++i] ?? p.acmeEmail;
    else if (a === '--dns-provider') p.dnsProvider = argv[++i] ?? p.dnsProvider;
    else if (a === '--set') {
      const kv = argv[++i] ?? '';
      const eq = kv.indexOf('=');
      if (eq < 1) fail(`--set expects KEY=VALUE, got "${kv}"`);
      p.overrides[kv.slice(0, eq)] = kv.slice(eq + 1);
    } else if (a === '-h' || a === '--help') {
      usage();
      process.exit(EXIT.ok);
    } else if (a.startsWith('-')) fail(`unknown flag ${a}`);
    else rest.push(a);
  }
  p.cmd = rest[0] ?? '';
  return p;
}

function usage(): void {
  console.log(
    [
      'pstack — declarative lifecycle for ephemeral preview stacks',
      '',
      'Usage: pstack <up|down|verify|status|validate|init|serve> [flags]',
      '',
      'Flags:',
      '  -f, --file <path>   spec file (default: preview.yml)',
      '  -n, --dry-run       print what would run, change nothing',
      '  -v, --verbose       echo commands and their output',
      '  -q, --quiet         suppress per-step chatter',
      '      --set K=V       override/define a variable (repeatable)',
      '      --no-verify     down: skip the post-teardown leak check',
      '      --force         down: allow tearing down a `kind: shared` deployment',
      '',
      '',
      'init flags: --domain <preview.example.com>  --acme-email <you@example.com>',
      '            --dns-provider <lego-code>      (DNS-01 token via PSTACK_DNS_TOKEN)',
      '',
      'serve env:  PSTACK_TOKEN (required to bind off-loopback) · PSTACK_PORT (7878)',
      '            PSTACK_HOST (127.0.0.1) · PSTACK_DATA (/var/lib/pstack)',
      '',
      'Exit: 0 ok · 1 failed · 2 leaked · 3 bad spec/usage',
    ].join('\n'),
  );
}

function fail(msg: string): never {
  console.error(`pstack: ${msg}`);
  process.exit(EXIT.usage);
}

const args = parseArgs(process.argv.slice(2));
if (!args.cmd) {
  usage();
  process.exit(EXIT.usage);
}

// `init` and `serve` are control-plane commands: they operate on the HOST and on the registry,
// not on a single spec file, so neither should fail because ./preview.yml happens to be absent.
const SPEC_FREE = new Set(['init', 'serve']);

let spec: Awaited<ReturnType<typeof loadSpec>> | undefined;
if (!SPEC_FREE.has(args.cmd)) {
  try {
    spec = await loadSpec(args.file, { ...process.env, ...args.overrides });
  } catch (err) {
    if (err instanceof SpecError) fail(err.message);
    throw err;
  }
}

const runner = createRunner({
  dryRun: args.dryRun,
  level: args.level,
  baseEnv: { ...process.env, ...(spec?.env ?? {}) } as Record<string, string>,
});

if (args.level !== 'quiet' && spec) {
  console.log(`stack: ${spec.stack}${args.dryRun ? '  (dry-run)' : ''}`);
}

switch (args.cmd) {
  case 'validate': {
    console.log(
      `✓ spec parses — kind: ${spec!.kind}, ${spec!.axes.length} axis/axes, stack "${spec!.stack}"`,
    );
    for (const r of spec!.requires) console.log(`  requires: ${r.name}`);
    if (spec!.compose) {
      console.log(`  compose: ${spec!.compose.file} [${spec!.compose.profiles.join(', ') || 'no profiles'}]`);
    }
    for (const a of spec!.axes) {
      const has = (['up', 'down', 'assert_gone', 'assert_live'] as const)
        .filter((k) => a[k])
        .join(', ');
      console.log(`  - ${a.name}: ${has}`);
    }
    for (const w of warnings) console.log(`  ! ${w}`);
    process.exit(EXIT.ok);
  }

  case 'up': {
    const r = await up(spec!, runner);
    console.log(report(r));
    process.exit(r.ok ? EXIT.ok : EXIT.failed);
  }

  case 'down': {
    const r = await down(spec!, runner, { verify: !args.noVerify, force: args.force });
    console.log(report(r));
    // A leak is reported distinctly from a teardown error: the axes' `down` hooks are best-effort by
    // design, so "down ran but something survived" is the interesting signal.
    const leaked = r.steps.some((s) => s.phase === 'assert_gone' && !s.ok);
    process.exit(leaked ? EXIT.leaked : r.ok ? EXIT.ok : EXIT.failed);
  }

  case 'verify': {
    const r = await verify(spec!, runner);
    console.log(report(r));
    process.exit(r.ok ? EXIT.ok : EXIT.leaked);
  }

  case 'status': {
    console.log(await status(spec!, runner));
    process.exit(EXIT.ok);
  }

  case 'init': {
    const { init } = await import('./init.ts');
    const { dataDir } = await import('./registry.ts');
    if (!args.domain) fail('init needs --domain (or PSTACK_DOMAIN), e.g. preview.example.com');
    if (!args.acmeEmail) fail('init needs --acme-email (or PSTACK_ACME_EMAIL)');
    if (!args.dnsProvider) fail('init needs --dns-provider (or PSTACK_DNS_PROVIDER), e.g. cloudflare');
    await init({
      dataDir: dataDir(),
      domain: args.domain,
      acmeEmail: args.acmeEmail,
      dnsProvider: args.dnsProvider,
      token: process.env.PSTACK_DNS_TOKEN,
      dryRun: args.dryRun,
      runner,
    });
    process.exit(EXIT.ok);
  }

  case 'serve': {
    const { createServer } = await import('./api.ts');
    const { dataDir } = await import('./registry.ts');
    const token = process.env.PSTACK_TOKEN;
    const port = Number(process.env.PSTACK_PORT ?? 7878);
    // Safety interlock: an unauthenticated instance of an API that can delete databases must not be
    // reachable off-box. Without a token we pin to loopback and say so, rather than trusting the
    // operator to remember.
    const wantHost = process.env.PSTACK_HOST ?? '127.0.0.1';
    const host = token ? wantHost : '127.0.0.1';
    if (!token && wantHost !== '127.0.0.1') {
      console.error(
        `pstack: refusing to bind ${wantHost} without PSTACK_TOKEN set — this API can destroy\n` +
          `        infrastructure. Set PSTACK_TOKEN=<secret> to listen off-loopback.`,
      );
      process.exit(EXIT.usage);
    }

    const uiDir = new URL('../ui', import.meta.url).pathname;
    createServer({ dataDir: dataDir(), port, host, token, uiDir });
    console.log(`pstack api  http://${host}:${port}`);
    console.log(`  registry: ${dataDir()}/deployments`);
    console.log(
      token
        ? '  auth: bearer token required for mutating routes'
        : '  auth: NONE — bound to loopback only (set PSTACK_TOKEN to expose)',
    );
    break; // Bun.serve keeps the process alive
  }

  default:
    fail(`unknown command "${args.cmd}" (try: up, down, verify, status, validate, init, serve)`);
}
