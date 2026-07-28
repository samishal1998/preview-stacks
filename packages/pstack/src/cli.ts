#!/usr/bin/env bun
/**
 * pstack — declarative lifecycle for ephemeral preview stacks.
 *
 *   pstack up        provision every axis, then compose up
 *   pstack down      compose down (all profiles), destroy axes in reverse, then verify
 *   pstack verify    assert every axis is gone; non-zero exit if anything leaked
 *   pstack status    what is running for this stack
 *   pstack validate  parse the spec, resolve interpolation, report warnings
 *   pstack cloud-init   fill the cloud-config template and print ready-to-boot user-data
 *   pstack build-image  build the control image from this installed package (--ui for the SPA)
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
  challenge: 'http01' | 'dns01';
  tag: string;
  ui: 'basic' | 'advanced';
  uiImage: boolean;
  uiDist: string;
  sshKey: string;
  password: string;
  configRepo: string;
  out: string;
  yes: boolean;
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
    // HTTP-01 by default: it needs no DNS credential, so `init` works with nothing but a domain
    // pointed at the box. See the ceiling documented on InitOptions.challenge before scaling PRs.
    challenge: (process.env.PSTACK_CHALLENGE as 'http01' | 'dns01') || 'http01',
    // Matches init's PSTACK_IMAGE default, so `build-image` then `init` need no flags at all.
    tag: process.env.PSTACK_IMAGE ?? 'pstack:local',
    // Basic by default: it is embedded in the API bundle, so it costs no extra container and
    // cannot be out of date relative to the API it talks to.
    ui: (process.env.PSTACK_UI as 'basic' | 'advanced') || 'basic',
    uiImage: false,
    uiDist: '',
    sshKey: process.env.PSTACK_SSH_KEY ?? '',
    password: process.env.PSTACK_DASHBOARD_PASSWORD ?? '',
    configRepo: '',
    out: '',
    yes: false,
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
    else if (a === '--tag') p.tag = argv[++i] ?? p.tag;
    else if (a === '--ui-dist') p.uiDist = argv[++i] ?? p.uiDist;
    else if (a === '--ssh-key') p.sshKey = argv[++i] ?? p.sshKey;
    else if (a === '--password') p.password = argv[++i] ?? p.password;
    else if (a === '--config-repo') p.configRepo = argv[++i] ?? p.configRepo;
    else if (a === '-o' || a === '--out') p.out = argv[++i] ?? p.out;
    else if (a === '-y' || a === '--yes') p.yes = true;
    else if (a === '--ui') {
      // `build-image --ui` is a switch (build the SPA image); `init --ui <mode>` takes a value.
      // Peek rather than always consuming, so neither form has to be spelled differently.
      const next = argv[i + 1];
      if (next === 'basic' || next === 'advanced') {
        p.ui = next;
        i++;
      } else if (next !== undefined && !next.startsWith('-')) {
        fail(`--ui must be basic or advanced, got "${next}"`);
      } else {
        p.uiImage = true;
      }
    }
    else if (a === '--challenge') {
      const c = argv[++i] ?? '';
      if (c !== 'http01' && c !== 'dns01') fail(`--challenge must be http01 or dns01, got "${c}"`);
      p.challenge = c;
    }
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
      'Usage: pstack <up|down|verify|status|validate|cloud-init|build-image|init|serve> [flags]',
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
      'build-image: --tag <name:tag>               (default pstack:local, = PSTACK_IMAGE)',
      '             --ui                           build the advanced UI image instead',
      '             --ui-dist <path>               use a built UI dist from a specific path',
      '',
      'init flags: --domain <preview.example.com>  --acme-email <you@example.com>',
      '            --challenge http01|dns01        (default http01 — no DNS credential needed)',
      '            --ui basic|advanced             (default basic — embedded, no extra container)',
      '            --dns-provider <lego-code>      (dns01 only; token via PSTACK_DNS_TOKEN)',
      '',
      'cloud-init: --domain --acme-email --ssh-key [--password] [--challenge] [--ui]',
      '            [--config-repo <git-url>]  [-o file]  [-y]   (-y = never prompt)',
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
const SPEC_FREE = new Set(['init', 'serve', 'build-image', 'cloud-init']);

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

  case 'cloud-init': {
    const { renderCloudInit, randomPassword, ask, askOptional, CloudInitError } = await import('./cloudinit.ts');
    try {
      // Every field: flag first, then a prompt — unless -y, which makes a missing required flag an
      // error instead. That is what makes the command usable from a script without it hanging on a
      // prompt nobody can see.
      const need = (label: string, given: string, fallback?: string): string => {
        if (given) return given;
        if (args.yes) {
          if (fallback) return fallback;
          fail(`--yes was given but "${label}" is missing — pass it as a flag`);
        }
        return ask(label, fallback);
      };

      const domain = need('Preview domain (e.g. preview.example.com)', args.domain);
      const acmeEmail = need("Let's Encrypt contact email", args.acmeEmail);
      const sshKey = need('SSH public key line (ssh-ed25519 AAAA… you@laptop)', args.sshKey);
      // Generated by default: a password typed at a prompt tends to be one already in use
      // elsewhere, and this one ends up in instance metadata.
      const generated = !args.password;
      const dashboardPassword = args.password || randomPassword();
      // Optional, so EOF means "skip" rather than an error — otherwise piping answers in produces
      // no file at all just because the last one was omitted.
      const configRepo = args.configRepo || (args.yes ? '' : askOptional('Config repo git URL'));

      const yaml = renderCloudInit({
        domain,
        acmeEmail,
        sshKey,
        dashboardPassword,
        challenge: args.challenge,
        dnsProvider: args.dnsProvider || undefined,
        ui: args.ui,
        configRepo: configRepo || undefined,
      });

      if (args.out) {
        await Bun.write(args.out, yaml);
        console.error(`wrote ${args.out}`);
      } else {
        // stdout, so `pstack cloud-init … > user-data.yaml` composes. Everything else this command
        // says goes to stderr for the same reason.
        console.log(yaml);
      }

      console.error('');
      console.error(`  Traefik dashboard:  admin / ${dashboardPassword}${generated ? '  (generated)' : ''}`);
      console.error('  Save it now — it is hashed into the file, not recoverable from it.');
      console.error('');
      console.error('  This file carries that password and your ACME email, and a provider stores');
      console.error('  user-data where any process on the instance can read it. Do not commit it.');
      console.error('');
      console.error(`  DNS first:  ${domain}  and  *.${domain}   A -> <server-ip>`);
      if (args.challenge === 'http01') {
        console.error('  HTTP-01 needs port 80 reachable from the internet.');
      }
    } catch (err) {
      if (err instanceof CloudInitError) fail(err.message);
      throw err;
    }
    process.exit(EXIT.ok);
  }

  case 'build-image': {
    const { buildImage, DEFAULT_UI_IMAGE_TAG } = await import('./image.ts');
    // Default the tag to whichever image is being built, so the two never collide when a user
    // builds both without thinking about tags.
    const tag = args.uiImage && args.tag === 'pstack:local' ? DEFAULT_UI_IMAGE_TAG : args.tag;
    try {
      await buildImage({
        tag,
        runner,
        dryRun: args.dryRun,
        ui: args.uiImage,
        uiDist: args.uiDist || undefined,
      });
    } catch (err) {
      fail((err as Error).message);
    }
    process.exit(EXIT.ok);
  }

  case 'init': {
    const { init } = await import('./init.ts');
    const { dataDir } = await import('./registry.ts');
    if (!args.domain) fail('init needs --domain (or PSTACK_DOMAIN), e.g. preview.example.com');
    if (!args.acmeEmail) fail('init needs --acme-email (or PSTACK_ACME_EMAIL)');
    // Only dns01 needs a provider; http01 answers on port 80 with no credential at all.
    if (args.challenge === 'dns01' && !args.dnsProvider) {
      fail('--challenge dns01 needs --dns-provider (or PSTACK_DNS_PROVIDER), e.g. cloudflare');
    }
    await init({
      dataDir: dataDir(),
      domain: args.domain,
      acmeEmail: args.acmeEmail,
      dnsProvider: args.dnsProvider || undefined,
      challenge: args.challenge,
      ui: args.ui,
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

    createServer({ dataDir: dataDir(), port, host, token });
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
    fail(`unknown command "${args.cmd}" (try: up, down, verify, status, validate, cloud-init, build-image, init, serve)`);
}
