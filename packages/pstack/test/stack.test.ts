import { describe, expect, test } from 'bun:test';
import { parseSpec, SpecError, interpolate, warnings } from '../src/spec.ts';
import { captureOutputs, createRunner, type RunResult, type Runner } from '../src/exec.ts';
import { down, up, verify } from '../src/stack.ts';
import { nullSink } from '../src/log.ts';
import { init } from '../src/init.ts';
import { buildImage, controlDockerfile, uiDockerfile } from '../src/image.ts';
import { SpecStore, findRequiredVars } from '../src/specs.ts';
import { renderCloudInit, randomPassword } from '../src/cloudinit.ts';
import { Registry } from '../src/registry.ts';
import { displayDeclared, isSecretName, mask, redactText } from '../src/redact.ts';
import { composeDown, composeUp, shq } from '../src/compose.ts';
import { subdomainVarName, wildcardRule } from '../src/subdomains.ts';
import { createServer } from '../src/api.ts';
import { RoutingStore, RoutingError, validateRoutingContent } from '../src/routing.ts';

/** A runner that records commands and returns scripted results — for ordering/flow assertions. */
function fakeRunner(fail: (cmd: string) => boolean = () => false, stdout = ''): Runner & { log: string[] } {
  const log: string[] = [];
  return {
    dryRun: false,
    log,
    async run(cmd: string): Promise<RunResult> {
      log.push(cmd.trim());
      const bad = fail(cmd);
      return { ok: !bad, code: bad ? 1 : 0, stdout, stderr: bad ? 'boom' : '', skipped: false };
    },
  };
}

const spec = (yaml: string, env: Record<string, string> = { PR: '7' }) => parseSpec(yaml, env);

describe('spec', () => {
  test('interpolates and exposes STACK', () => {
    const s = spec(`
stack: pr-\${PR}
env:
  HOST: app-\${PR}.example.com
axes:
  - name: a
    up: echo \${HOST} \${STACK}
`);
    expect(s.stack).toBe('pr-7');
    expect(s.env.HOST).toBe('app-7.example.com');
    // STACK is available to hooks without them reconstructing it
    expect(s.axes[0]!.up).toBe('echo app-7.example.com pr-7');
  });

  test('an undefined variable is a hard error, not an empty string', () => {
    // The bug this prevents: `pr-${PR}` with PR unset silently becomes `pr-`, which every PR
    // then shares — cross-PR collision instead of isolation.
    expect(() => spec('stack: pr-${NOPE}\naxes: []', {})).toThrow(SpecError);
    expect(() => interpolate('${A}', {}, 'x')).toThrow(/undefined variable/);
  });

  test('rejects a stack name that cannot be a compose project / hostname label', () => {
    expect(() => spec('stack: PR_Seven!\naxes: []')).toThrow(/must match/);
  });

  test('rejects duplicate axis names and empty axes', () => {
    expect(() => spec('stack: s\naxes:\n  - name: a\n    up: x\n  - name: a\n    up: y')).toThrow(/duplicate/);
    expect(() => spec('stack: s\naxes:\n  - name: a')).toThrow(/defines no up\/down/);
  });

  test('warns when an axis can be created but never proven gone', () => {
    spec('stack: s\naxes:\n  - name: a\n    up: touch f');
    expect(warnings.join()).toMatch(/no `assert_gone`/);
  });

  test('unsupported version is rejected', () => {
    expect(() => spec('version: 2\nstack: s\naxes: []')).toThrow(/unsupported version/);
  });
});

describe('up', () => {
  const two = `
stack: pr-\${PR}
axes:
  - name: first
    up: echo one
  - name: second
    up: echo two
`;

  test('provisions in declaration order', async () => {
    const r = fakeRunner();
    await up(spec(two), r);
    expect(r.log).toEqual(['echo one', 'echo two']);
  });

  test('fails fast — a failed axis stops the rest and never reaches compose', async () => {
    const r = fakeRunner((c) => c.includes('one'));
    const out = await up(spec(two), r);
    expect(out.ok).toBe(false);
    expect(r.log).toEqual(['echo one']); // second never ran
  });

  test('captures KEY=VALUE from a provision hook into later env', () => {
    expect(captureOutputs('noise\nDATABASE_URL=postgres://x\nlower=ignored\n')).toEqual({
      DATABASE_URL: 'postgres://x',
    });
  });

  test('assert_live failure fails the up', async () => {
    const s = spec(`
stack: s
axes:
  - name: db
    up: echo made
    assert_live: false
`);
    const r = fakeRunner((c) => c.trim() === 'false');
    const out = await up(s, r);
    expect(out.ok).toBe(false);
    expect(out.steps.at(-1)!.phase).toBe('assert_live');
  });
});

describe('down', () => {
  const three = `
stack: s
axes:
  - name: db
    down: echo rm-db
    assert_gone: "true"
  - name: queue
    down: echo rm-queue
    assert_gone: "true"
  - name: images
    down: echo rm-images
    assert_gone: "true"
`;

  test('destroys in REVERSE declaration order', async () => {
    const r = fakeRunner();
    await down(spec(three), r, { verify: false });
    expect(r.log).toEqual(['echo rm-images', 'echo rm-queue', 'echo rm-db']);
  });

  test('is best-effort: one failing destroy does not abort the others', async () => {
    // The reason: aborting halfway leaves MORE garbage than continuing.
    const r = fakeRunner((c) => c.includes('rm-queue'));
    const out = await down(spec(three), r, { verify: false });
    expect(r.log).toEqual(['echo rm-images', 'echo rm-queue', 'echo rm-db']);
    expect(out.ok).toBe(true); // destroy failures are non-fatal…
    expect(out.steps.some((s) => s.message?.startsWith('non-fatal'))).toBe(true); // …but recorded
  });
});

describe('verify — the leak gate', () => {
  test('a surviving resource makes verify fail', async () => {
    const s = spec('stack: s\naxes:\n  - name: db\n    down: "true"\n    assert_gone: "false"');
    const r = fakeRunner((c) => c.trim() === 'false');
    const out = await verify(s, r);
    expect(out.ok).toBe(false);
    expect(out.steps[0]!.message).toMatch(/LEAKED/);
  });

  test('an axis with no assert_gone is reported unverifiable, not silently passed', async () => {
    const s = spec('stack: s\naxes:\n  - name: db\n    down: "true"');
    const out = await verify(s, fakeRunner());
    expect(out.ok).toBe(true);
    expect(out.steps[0]!.message).toMatch(/unverifiable/);
    expect(out.steps[0]!.skipped).toBe(true);
  });

  test('down runs verify by default and surfaces the leak', async () => {
    const s = spec('stack: s\naxes:\n  - name: db\n    down: "true"\n    assert_gone: "false"');
    const r = fakeRunner((c) => c.trim() === 'false');
    const out = await down(s, r);
    expect(out.ok).toBe(false);
    expect(out.steps.some((x) => x.phase === 'assert_gone' && !x.ok)).toBe(true);
  });
});

describe('compose invocation', () => {
  const s = spec(`
stack: pr-\${PR}
compose:
  file: dc.yml
  profiles: [backend, frontend]
axes: []
`);

  test('up enables only the selected profiles', async () => {
    const r = fakeRunner();
    await up(s, r);
    expect(r.log[0]).toContain("--profile 'backend' --profile 'frontend'");
    expect(r.log[0]).toContain("-p 'pr-7'");
    expect(r.log[0]).toContain('--remove-orphans');
  });

  test('down enables EVERY profile — the network-leak fix', async () => {
    // Compose treats a non-enabled profile's services as absent, so omitting one leaves that
    // profile's resources (notably the project default network) behind forever.
    const r = fakeRunner();
    await down(s, r, { verify: false });
    expect(r.log[0]).toContain("--profile 'backend' --profile 'frontend'");
    expect(r.log[0]).toContain('down -v --remove-orphans');
  });

  test('shq makes hostile values shell-safe', () => {
    expect(shq("a'b")).toBe(`'a'\\''b'`);
    expect(shq('a b$c')).toBe(`'a b$c'`);
  });
});

describe('real shell execution', () => {
  test('runs a command and captures output', async () => {
    const r = createRunner({ dryRun: false });
    const out = await r.run('echo REAL=yes');
    expect(out.ok).toBe(true);
    expect(captureOutputs(out.stdout)).toEqual({ REAL: 'yes' });
  });

  test('a non-zero exit is reported, not thrown', async () => {
    const out = await createRunner({ dryRun: false }).run('exit 3');
    expect(out.ok).toBe(false);
    expect(out.code).toBe(3);
  });

  test('dry-run executes nothing', async () => {
    const marker = `${process.env.TMPDIR ?? '/tmp'}/pstack-dryrun-${process.pid}`;
    await createRunner({ dryRun: true, level: 'quiet' }).run(`touch ${marker}`);
    expect(await Bun.file(marker).exists()).toBe(false);
  });

  test('end-to-end leak detection against the real filesystem', async () => {
    // The scenario the tool exists for: `down` "succeeds" but the resource survives.
    const f = `${process.env.TMPDIR ?? '/tmp'}/pstack-leak-${process.pid}`;
    await createRunner({ dryRun: false }).run(`touch ${f}`);
    const s = spec(`
stack: s
axes:
  - name: leftover
    down: "true"                 # pretends to clean up
    assert_gone: "! test -e ${f}"
`);
    const runner = createRunner({ dryRun: false, level: 'quiet' });
    const leaked = await verify(s, runner);
    expect(leaked.ok).toBe(false); // caught it

    await runner.run(`rm -f ${f}`);
    const clean = await verify(s, runner);
    expect(clean.ok).toBe(true); // and passes once actually gone
  });
});

describe('assert_gone lint — protecting the core promise', () => {
  const parse = (yaml: string) => parseSpec(yaml, { PR: '1' });

  test('flags a bare `! probe` with no reachability guard', () => {
    // `! docker exec …` exits 0 when docker itself is missing, so "cannot tell" reads as "gone".
    parse('stack: s\naxes:\n  - name: q\n    up: "true"\n    assert_gone: "! docker exec c probe"');
    expect(warnings.join('\n')).toMatch(/no reachability guard/);
  });

  test('accepts a guarded, fail-closed assert', () => {
    parse(`
stack: s
axes:
  - name: q
    up: "true"
    assert_gone: |
      docker info >/dev/null 2>&1 || exit 1
      ! docker exec c probe
`);
    expect(warnings.join('\n')).not.toMatch(/reachability guard/);
  });

  test('flags `|| true` in an assert, which can never fail', () => {
    parse('stack: s\naxes:\n  - name: q\n    up: "true"\n    assert_gone: "probe || true"');
    expect(warnings.join('\n')).toMatch(/always pass/);
  });
});

describe('deployment kinds', () => {
  const parse = (yaml: string, env: Record<string, string> = { PR: '1' }) => parseSpec(yaml, env);

  test('defaults to isolated', () => {
    expect(parse('stack: s\naxes: []').kind).toBe('isolated');
  });

  test('rejects an unknown kind', () => {
    expect(() => parse('kind: wat\nstack: s\naxes: []')).toThrow(/must be "shared" or "isolated"/);
  });

  test('a shared deployment cannot declare axes', () => {
    // Axes isolate one tenant from another; a singleton has no such concern. Almost always a spec
    // that meant `kind: isolated`.
    expect(() =>
      parse('kind: shared\nstack: s\naxes:\n  - name: db\n    up: "true"'),
    ).toThrow(/cannot declare axes/);
  });

  test('warns when an isolated deployment has no axes', () => {
    parse('kind: isolated\nstack: s\ncompose: {file: dc.yml, profiles: []}\naxes: []');
    expect(warnings.join('\n')).toMatch(/nothing per-tenant/);
  });

  test('down REFUSES a shared deployment without force', async () => {
    // The guard exists because `compose down -v` destroys volumes every tenant depends on:
    // a TLS store, a shared database, admin credentials.
    const s = parse('kind: shared\nstack: sharedsvc\ncompose: {file: dc.yml, profiles: []}');
    const r = fakeRunner();
    const out = await down(s, r, {}, nullSink());
    expect(out.ok).toBe(false);
    expect(out.steps[0]!.message).toMatch(/refused/);
    expect(r.log).toEqual([]); // nothing ran at all
  });

  test('down proceeds on a shared deployment with force', async () => {
    const s = parse('kind: shared\nstack: sharedsvc\ncompose: {file: dc.yml, profiles: []}');
    const r = fakeRunner();
    const out = await down(s, r, { force: true, verify: false }, nullSink());
    expect(out.ok).toBe(true);
    expect(r.log[0]).toContain('down -v');
  });

  test('isolated deployments are not guarded', async () => {
    const s = parse('kind: isolated\nstack: pr-1\ncompose: {file: dc.yml, profiles: []}\naxes: []');
    const r = fakeRunner();
    await down(s, r, { verify: false }, nullSink());
    expect(r.log[0]).toContain('down -v');
  });
});

describe('requires — preflight', () => {
  const yaml = `
stack: pr-1
requires:
  - name: ingress network
    assert: docker network inspect net
    hint: run \`pstack init\` first
axes:
  - name: db
    up: echo made-db
`;

  test('runs before any axis and blocks up when unmet', async () => {
    // Without this, a missing shared dependency surfaces as whatever error an axis hook's CLI
    // printed — which tells you nothing about the real cause.
    const r = fakeRunner((c) => c.includes('network inspect'));
    const out = await up(parseSpec(yaml, {}), r, nullSink());
    expect(out.ok).toBe(false);
    expect(out.steps[0]!.phase).toBe('requires');
    expect(out.steps[0]!.message).toMatch(/unmet — run `pstack init` first/);
    expect(r.log).toEqual(['docker network inspect net']); // the axis never ran
  });

  test('when met, axes proceed', async () => {
    const r = fakeRunner();
    const out = await up(parseSpec(yaml, {}), r, nullSink());
    expect(out.ok).toBe(true);
    expect(r.log).toEqual(['docker network inspect net', 'echo made-db']);
  });
});

describe('init — ACME challenge rendering', () => {
  // These assert the CONTROL STACK MANIFEST, which is the artifact a host actually runs. Rendering
  // it wrong is invisible until Traefik is up and certificates silently never arrive.

  /**
   * A runner that reports success for everything, so preconditions pass without Docker — and
   * answers the health probe with `healthy`, otherwise init's wait loop polls for 60s and the test
   * times out rather than failing.
   */
  const okRunner = (): Runner => ({
    dryRun: false,
    async run(cmd: string) {
      const stdout = cmd.includes('State.Health.Status') ? 'healthy\n' : '';
      return { ok: true, code: 0, stdout, stderr: '', skipped: false };
    },
  });

  const render = async (extra: Record<string, unknown>) => {
    const dir = `${process.env.TMPDIR ?? '/tmp'}/pstack-init-${Math.abs(Date.now() % 1e9)}-${Object.keys(extra).length}`;
    await init({
      dataDir: dir,
      domain: 'preview.example.com',
      acmeEmail: 'ops@example.com',
      challenge: 'http01',
      dryRun: false,
      runner: okRunner(),
      ...(extra as { challenge?: 'http01' | 'dns01' }),
    } as Parameters<typeof init>[0]);
    return Bun.file(`${dir}/control/docker-compose.yml`).text();
  };

  test('http01 is the default and needs no DNS credential', async () => {
    const yaml = await render({});
    expect(yaml).toContain('acme.httpchallenge=true');
    expect(yaml).toContain('acme.httpchallenge.entrypoint=web');
    expect(yaml).not.toContain('acme.dnschallenge=true');
    // HTTP-01 cannot issue a wildcard, so no router may ask for one.
    expect(yaml).not.toContain('tls.domains[0].sans');
    // …and each hostname must resolve its own cert, so routers DO carry certresolver.
    expect(yaml).toContain('routers.pstack-ui.tls.certresolver=le');
  });

  test('dns01 renders the wildcard on exactly one router', async () => {
    const yaml = await render({ challenge: 'dns01', dnsProvider: 'hetzner' });
    expect(yaml).toContain('acme.dnschallenge=true');
    expect(yaml).toContain('tls.domains[0].sans=*.${DOMAIN}');
    expect(yaml).not.toContain('acme.httpchallenge=true');
    // Exactly one router requests it; a second would order a separate cert and burn the
    // ~50-certs-per-registered-domain-per-week limit.
    expect(yaml.match(/tls\.domains\[0\]\.main/g)?.length).toBe(1);
  });

  test('both modes route control.<domain> and api.<domain> at one service', async () => {
    for (const opts of [{}, { challenge: 'dns01' as const, dnsProvider: 'hetzner' }]) {
      const yaml = await render(opts);
      expect(yaml).toContain('routers.pstack-ui.rule=Host(`control.${DOMAIN}`)');
      expect(yaml).toContain('routers.pstack-api.rule=Host(`api.${DOMAIN}`)');
      // One service behind both, so the UI's relative /api/… calls stay same-origin (no CORS).
      expect(yaml).toContain('routers.pstack-ui.service=pstack');
      expect(yaml).toContain('routers.pstack-api.service=pstack');
    }
  });

  test('no unrendered markers survive', async () => {
    const yaml = await render({});
    expect(yaml).not.toContain('__ACME_CHALLENGE__');
    expect(yaml).not.toContain('__ACME_ROUTER_TLS__');
  });
});

describe('redaction — what a browser may see', () => {
  test('secret-looking names are masked, topology names are shown', () => {
    for (const k of ['DATABASE_URL', 'PSTACK_TOKEN', 'API_AUTH_KEY', 'STRIPE_SECRET', 'DSN', 'SESSION_KEY']) {
      expect(isSecretName(k)).toBe(true);
    }
    for (const k of ['PR', 'PR_NUMBER', 'PREVIEW_DOMAIN', 'PORT', 'IMAGE_TAG', 'GIT_SHA', 'LOG_LEVEL']) {
      expect(isSecretName(k)).toBe(false);
    }
  });

  test('deny by default — an unrecognised name is treated as secret', () => {
    // The asymmetry that drives this: wrongly masking a domain is an annoyance, wrongly revealing
    // a connection string in a browser tab is a breach.
    expect(isSecretName('SOMETHING_NOBODY_ANTICIPATED')).toBe(true);
  });

  test('a secret name wins over a safe-looking suffix', () => {
    // `_IMAGE` is on the safe list, but this is a pull secret.
    expect(isSecretName('REGISTRY_IMAGE_PULL_SECRET')).toBe(true);
  });

  test('mask reveals no prefix of the real value', () => {
    // A deliberately fake-looking fixture: a realistic `sk_live_…` shape trips secret scanners on
    // every commit, and the property under test (no prefix survives) does not need realism.
    const m = mask('NOT-A-REAL-TOKEN-0123456789');
    expect(m).not.toContain('NOT-A-REAL');
    expect(m).toMatch(/^•+$/);
  });

  test('only DECLARED vars are rendered, never the ambient environment', () => {
    // The whole point: Stack.env holds every secret the process has.
    const env = { PR: '7', DATABASE_URL: 'postgres://u:p@h/db', PSTACK_TOKEN: 'supersecrettoken' };
    const out = displayDeclared(['PR', 'DATABASE_URL'], env);
    expect(out.map((v) => v.key)).toEqual(['PR', 'DATABASE_URL']);
    expect(JSON.stringify(out)).not.toContain('supersecrettoken');
    expect(JSON.stringify(out)).not.toContain('postgres://u:p@h/db');
    expect(out[0]!.value).toBe('7');
    expect(out[1]!.length).toBe('postgres://u:p@h/db'.length); // shape without content
  });

  test('redactText strips URL passwords and secret assignments from output', () => {
    const t = 'connecting to postgres://admin:hunter2@db.internal/app with API_TOKEN=abc123xyz';
    const r = redactText(t);
    expect(r).not.toContain('hunter2');
    expect(r).not.toContain('abc123xyz');
    expect(r).toContain('postgres://admin:••••@db.internal/app');
  });

  test('redactText masks known secret values wherever they appear', () => {
    const r = redactText('token is supersecrettoken here', ['supersecrettoken']);
    expect(r).not.toContain('supersecrettoken');
  });
});

describe('build-image — the global install must not be a dead end', () => {
  // The bug this closes: the published package ships only `dist/`, so a globally-installed pstack
  // had no Dockerfile to build the control image from and no registry to pull it from, while
  // `init` refuses to run without it. The way out is that `dist/` IS the whole application.
  const okRunner = (): Runner & { log: string[] } => {
    const log: string[] = [];
    return {
      dryRun: false,
      log,
      async run(cmd: string) {
        log.push(cmd);
        return { ok: true, code: 0, stdout: '', stderr: '', skipped: false };
      },
    };
  };

  /**
   * A stand-in for an installed `dist/`. The real one only exists after `bun scripts/build.ts`, and
   * a test that depends on a build step it does not run is order-dependent — which is how this
   * suite passed locally (stale dist present) and failed on a clean CI checkout.
   */
  const fakeDist = async (): Promise<string> => {
    const dir = `${process.env.TMPDIR ?? '/tmp'}/pstack-fakedist-${process.pid}-${Math.trunc(performance.now() * 1000)}`;
    await Bun.write(`${dir}/cli.js`, '#!/usr/bin/env bun\nconsole.log("stub");\n');
    return dir;
  };

  test('builds the configured tag and cleans up its context', async () => {
    const r = okRunner();
    await buildImage({ tag: 'pstack:test', runner: r, dryRun: false, distDir: await fakeDist() });
    const cmd = r.log.find((c) => c.startsWith('docker build'));
    expect(cmd).toBeDefined();
    expect(cmd).toContain('"pstack:test"');
    // --pull, so a stale cached base image cannot silently pin an old Bun runtime.
    expect(cmd).toContain('--pull');
    // The context is a temp dir, never the install directory: an install may be read-only or
    // shared, and writing a Dockerfile into it would mutate an installed dependency.
    const ctx = /docker build --pull -t "[^"]+" "([^"]+)"/.exec(cmd!)?.[1];
    expect(ctx).toBeDefined();
    expect(ctx).toContain('pstack-image-');
    // Removed even on success — a build context left in /tmp is litter nobody looks for.
    expect(await Bun.file(`${ctx}/Dockerfile`).exists()).toBe(false);
  });

  test('the Dockerfile reaches docker byte-for-byte, never through a shell', async () => {
    // The regression: the default path built `printf '%s' <json> | docker build -`. JSON escaping
    // is not shell escaping — in double quotes `\n` stays two literal characters and every backtick
    // in the Dockerfile's own comments is command substitution, so `docker compose`'s help text got
    // spliced into line 37 and the build died on `unknown instruction: Define`.
    //
    // So: assert the file docker is handed is identical to what we generated, and that the command
    // does not carry the document at all.
    let written = '';
    const r: Runner = {
      dryRun: false,
      async run(cmd: string) {
        const ctx = /docker build --pull -t "[^"]+" "([^"]+)"/.exec(cmd)?.[1];
        expect(ctx).toBeDefined();
        expect(cmd).not.toContain('FROM'); // the document is not on the command line
        written = await Bun.file(`${ctx}/Dockerfile`).text();
        return { ok: true, code: 0, stdout: '', stderr: '', skipped: false };
      },
    };

    await buildImage({ tag: 'pstack:test', runner: r, dryRun: false });
    expect(written).toBe(controlDockerfile());
    expect(written).toContain('`docker compose`'); // the backticks that caused it, intact

    await buildImage({ tag: 'pstack-ui:test', runner: r, dryRun: false, ui: true });
    expect(written).toBe(uiDockerfile());
  });

  test('a failed build surfaces docker output instead of a bare exit code', async () => {
    const failing: Runner = {
      dryRun: false,
      async run() {
        return { ok: false, code: 1, stdout: '', stderr: 'no space left on device', skipped: false };
      },
    };
    await expect(
      buildImage({ tag: 'pstack:test', runner: failing, dryRun: false, distDir: await fakeDist() }),
    ).rejects.toThrow(/no space left on device/);
  });

  test('dry-run executes nothing', async () => {
    const r = okRunner();
    await buildImage({ tag: 'pstack:test', runner: r, dryRun: true, distDir: await fakeDist() });
    expect(r.log).toEqual([]);
  });
});

describe('named specs — store once, reference many', () => {
  const tmp = () => `${process.env.TMPDIR ?? '/tmp'}/pstack-specs-${process.pid}-${Math.trunc(performance.now() * 1000)}`;
  const SPEC = [
    'version: 1',
    'kind: isolated',
    'stack: pr-${PR}',
    'env:',
    '  TAG: ${GIT_SHA}',
    'axes:',
    '  - name: db',
    '    up: "true"',
    '    assert_gone: "true"',
    '',
  ].join('\n');

  test('reports the variables a caller must supply', () => {
    // Surfaced up front so a list view can say "this needs PR and GIT_SHA", instead of the caller
    // discovering them one 400 at a time.
    expect(findRequiredVars(SPEC)).toEqual(['GIT_SHA', 'PR']);
  });

  test('required vars are NOT satisfied by the server\'s own environment', () => {
    // Otherwise a spec would validate on a box where PR happens to be exported and fail on one
    // where it is not — a works-on-my-box that only appears in production.
    process.env.PSTACK_TEST_LEAKY = 'set';
    try {
      expect(findRequiredVars('version: 1\nstack: s-${PSTACK_TEST_LEAKY}\naxes: []')).toEqual([
        'PSTACK_TEST_LEAKY',
      ]);
    } finally {
      delete process.env.PSTACK_TEST_LEAKY;
    }
  });

  test('stores and reads back a spec with its kind', async () => {
    const store = new SpecStore(tmp());
    const stored = await store.put('web', SPEC, { description: 'the web app' });
    expect(stored.kind).toBe('isolated');
    expect(stored.requiredVars).toEqual(['GIT_SHA', 'PR']);
    expect((await store.list()).map((s) => s.name)).toEqual(['web']);
    expect(await store.source('web')).toBe(SPEC);
  });

  test('a malformed spec is rejected and nothing is written', async () => {
    const dir = tmp();
    const store = new SpecStore(dir);
    await expect(store.put('bad', 'axes: [oops')).rejects.toThrow(/spec rejected/);
    expect(await store.get('bad')).toBeNull();
  });

  test('rejects a name that could escape the store directory', async () => {
    const store = new SpecStore(tmp());
    await expect(store.put('../etc', SPEC)).rejects.toThrow(/invalid spec name/);
  });
});

describe('deployment variables are stored, not re-passed', () => {
  test('resolve uses stored vars, and request vars still win', async () => {
    // The footgun this closes: variables used to travel as query params, so `up` with PR=7 and a
    // later `down` with PR=8 tore down a DIFFERENT stack and orphaned the first.
    const dir = `${process.env.TMPDIR ?? '/tmp'}/pstack-reg-${process.pid}-${Math.trunc(performance.now() * 1000)}`;
    const reg = new Registry(dir);
    await reg.put('pr-7', 'version: 1\nstack: pr-${PR}\naxes: []', { vars: { PR: '7' } });

    const stored = await reg.resolve('pr-7');            // no variables supplied at all
    expect(stored.stack).toBe('pr-7');

    const overridden = await reg.resolve('pr-7', { PR: '9' });
    expect(overridden.stack).toBe('pr-9');
  });

  test('a redeploy supplying one variable does not drop the others', async () => {
    const dir = `${process.env.TMPDIR ?? '/tmp'}/pstack-reg2-${process.pid}-${Math.trunc(performance.now() * 1000)}`;
    const reg = new Registry(dir);
    const spec = 'version: 1\nstack: pr-${PR}\nenv:\n  R: ${REGION}\naxes: []';
    await reg.put('d', spec, { vars: { PR: '7', REGION: 'eu' } });
    await reg.put('d', spec, { vars: { PR: '8' } });      // only PR this time
    const s = await reg.resolve('d');
    expect(s.stack).toBe('pr-8');
    expect(s.env.R).toBe('eu');                            // REGION survived
  });
});

describe('the advanced UI is opt-in', () => {
  const okRunner = (): Runner => ({
    dryRun: false,
    async run(cmd: string) {
      return { ok: true, code: 0, stdout: cmd.includes('State.Health.Status') ? 'healthy\n' : '', stderr: '', skipped: false };
    },
  });
  const render = async (ui: 'basic' | 'advanced') => {
    const dir = `${process.env.TMPDIR ?? '/tmp'}/pstack-ui-${process.pid}-${ui}`;
    await init({
      dataDir: dir, domain: 'preview.example.com', acmeEmail: 'o@e.com',
      challenge: 'http01', ui, dryRun: false, runner: okRunner(),
    });
    return Bun.file(`${dir}/control/docker-compose.yml`).text();
  };

  test('basic adds no container at all', async () => {
    const yaml = await render('basic');
    // Absent, not merely disabled — the basic UI is embedded in the API bundle, so there is
    // nothing extra to run, build or keep current.
    expect(yaml).not.toContain('advanced-ui');
    expect(yaml).toContain('routers.pstack-ui.service=pstack');
  });

  test('advanced adds the container and repoints control.<domain> at it', async () => {
    const yaml = await render('advanced');
    expect(yaml).toContain('  advanced-ui:');
    expect(yaml).toContain('image: ${PSTACK_UI_IMAGE}');
    expect(yaml).toContain('routers.pstack-ui.service=advanced-ui');
    expect(yaml).toContain('services.advanced-ui.loadbalancer.server.port=80');
    // The API keeps api.<domain>, so a broken UI image never leaves the host with no interface.
    expect(yaml).toContain('routers.pstack-api.rule=Host(`api.${DOMAIN}`)');
  });

  test('both modes leave no unrendered markers', async () => {
    for (const ui of ['basic', 'advanced'] as const) {
      const yaml = await render(ui);
      expect(yaml).not.toContain('__CONTROL_UI_SERVICE__');
      expect(yaml).not.toContain('__ADVANCED_UI_SERVICE__');
    }
  });

  test('the rendered compose is valid YAML in both modes', async () => {
    // A marker replacement that broke indentation would otherwise only surface on the host, as a
    // compose parse error during `init`.
    for (const ui of ['basic', 'advanced'] as const) {
      const yaml = await render(ui);
      const proc = Bun.spawn(['ruby', '-ryaml', '-e', 'YAML.load(STDIN.read)'], { stdin: 'pipe', stderr: 'pipe' });
      proc.stdin.write(yaml); await proc.stdin.end();
      expect(await proc.exited).toBe(0);
    }
  });
});

describe('build-image --ui', () => {
  const okRunner = (): Runner & { log: string[] } => {
    const log: string[] = [];
    return {
      dryRun: false,
      log,
      async run(cmd: string) {
        log.push(cmd);
        return { ok: true, code: 0, stdout: '', stderr: '', skipped: false };
      },
    };
  };

  /** A stand-in for an installed @samyx/preview-stacks-ui: built assets plus the nginx config. */
  const fakeUiPackage = async (withConf = true): Promise<string> => {
    const root = `${process.env.TMPDIR ?? '/tmp'}/pstack-uipkg-${process.pid}-${Math.trunc(performance.now() * 1000)}`;
    await Bun.write(`${root}/dist/index.html`, '<!doctype html><title>ui</title>');
    if (withConf) await Bun.write(`${root}/nginx.conf`, 'server { listen 80; }');
    return `${root}/dist`;
  };

  test('builds the UI image from a package dist, taking nginx.conf beside it', async () => {
    const r = okRunner();
    await buildImage({ tag: 'pstack-ui:test', runner: r, dryRun: false, ui: true, uiDist: await fakeUiPackage() });
    const cmd = r.log.find((c) => c.startsWith('docker build'));
    expect(cmd).toContain('"pstack-ui:test"');
  });

  test('refuses a package with assets but no nginx.conf', async () => {
    // Without it the image would serve the SPA with no /api proxy and no deep-link fallback —
    // a container that starts, looks healthy, and is unusable.
    await expect(
      buildImage({ tag: 'x', runner: okRunner(), dryRun: false, ui: true, uiDist: await fakeUiPackage(false) }),
    ).rejects.toThrow(/no nginx.conf beside them/);
  });

  test('an explicit --ui-dist without built assets fails by name', async () => {
    await expect(
      buildImage({ tag: 'x', runner: okRunner(), dryRun: false, ui: true, uiDist: '/nonexistent/dist' }),
    ).rejects.toThrow(/no built UI at/);
  });
});

describe('cloud-init generation', () => {
  const base = {
    domain: 'preview.example.com',
    acmeEmail: 'ops@example.com',
    sshKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest key@host',
    dashboardPassword: 'deadbeef1234',
    challenge: 'http01' as const,
    ui: 'basic' as const,
  };

  const yamlOk = async (text: string): Promise<boolean> => {
    const proc = Bun.spawn(['ruby', '-ryaml', '-e', 'YAML.load(STDIN.read)'], { stdin: 'pipe', stderr: 'pipe' });
    proc.stdin.write(text); await proc.stdin.end();
    return (await proc.exited) === 0;
  };

  test('renders valid cloud-config with no placeholders left', async () => {
    const out = renderCloudInit(base);
    expect(out.startsWith('#cloud-config')).toBe(true);
    // Either of these makes cloud-init discard the whole file silently.
    expect(out).not.toContain('\t');
    expect(await yamlOk(out)).toBe(true);
    expect(out.match(/\{\{[A-Z_]+\}\}/g)).toBeNull();
  });

  test("Docker's own Go templates survive rendering", () => {
    // `{{.State.Health.Status}}` shares the delimiters. It is left alone because placeholder names
    // are SHOUT_CASE with no dots — if that ever changes, this breaks first.
    expect(renderCloudInit(base)).toContain('{{.State.Health.Status}}');
  });

  test('the fallback regexp escapes every dot in the domain', () => {
    // Unescaped, `.` matches any character, so the router would also claim
    // backend-pr-1Xpreview.example.com — a hostname belonging to someone else.
    expect(renderCloudInit(base)).toContain('preview\\\\.example\\\\.com');
  });

  /**
   * The `pstack init` invocation, taken from the PARSED runcmd list.
   *
   * Parsed, not regex-scraped: the template's prose explains `--challenge dns01` as the wildcard
   * upgrade path and mentions `pstack init` several times, so a text match finds documentation
   * and reports on the wrong thing entirely. Comments do not survive a YAML parse, which is
   * exactly the property wanted here.
   */
  const runcmd = (yaml: string): string[] =>
    ((Bun.YAML.parse(yaml) as { runcmd?: unknown[] }).runcmd ?? []).map((c) => String(c));
  const initCall = (yaml: string): string =>
    runcmd(yaml).find((c) => c.includes('pstack init')) ?? '';

  test('dns01 adds the challenge flags to the init call; http01 adds none', () => {
    const dns = initCall(renderCloudInit({ ...base, challenge: 'dns01', dnsProvider: 'hetzner' }));
    expect(dns).toContain('--challenge dns01');
    expect(dns).toContain('--dns-provider hetzner');
    expect(initCall(renderCloudInit(base))).not.toContain('--challenge');
  });

  test('advanced UI adds its install and image build', () => {
    const adv = renderCloudInit({ ...base, ui: 'advanced' });
    expect(initCall(adv)).toContain('--ui advanced');
    expect(runcmd(adv).some((c) => c.includes('build-image --ui'))).toBe(true);
    expect(runcmd(renderCloudInit(base)).some((c) => c.includes('build-image --ui'))).toBe(false);
  });

  test('no config repo drops the clone line rather than emitting an empty one', async () => {
    // `git clone  /opt/preview/config` would fail and abort the rest of cloud-init.
    const out = renderCloudInit(base);
    expect(out).not.toMatch(/git clone --depth 1\s+\/opt/);
    expect(await yamlOk(out)).toBe(true);
  });

  test('rejects inputs that would produce a broken or locked-out host', () => {
    expect(() => renderCloudInit({ ...base, domain: 'notadomain' })).toThrow(/hostname/);
    expect(() => renderCloudInit({ ...base, acmeEmail: 'nope' })).toThrow(/address/);
    // Optional — but a MALFORMED one is still refused, because it produces a booted host nobody
    // can log into, the one failure here with no cheap recovery.
    expect(() => renderCloudInit({ ...base, sshKey: 'my-key' })).toThrow(/authorized_keys/);
    expect(() => renderCloudInit({ ...base, challenge: 'dns01' })).toThrow(/provider/);
    // Interpolated into a single-quoted shell command.
    expect(() => renderCloudInit({ ...base, dashboardPassword: "a'b" })).toThrow(/single quote/);
  });

  test('omitting the ssh key omits the whole key list, not an empty one', async () => {
    // `ssh_authorized_keys:` with nothing under it parses fine and yields a user with no way in
    // and no error — worse than not writing the block at all. Providers inject their own key.
    const out = renderCloudInit({ ...base, sshKey: undefined });
    expect(out).not.toContain('ssh_authorized_keys');
    expect(await yamlOk(out)).toBe(true);
  });

  test('/opt/preview is created before it is chowned', () => {
    // It failed on a real boot: with no config repo there was no clone, so the directory the chown
    // targeted never existed and the step errored.
    const cmds = runcmd(renderCloudInit(base));
    const mk = cmds.findIndex((c) => c.includes('mkdir -p /opt/preview'));
    const ch = cmds.findIndex((c) => c.includes('chown -R preview:preview /opt/preview'));
    expect(mk).toBeGreaterThanOrEqual(0);
    expect(mk).toBeLessThan(ch);
  });

  test('generated passwords are shell-safe', () => {
    // Hex on purpose: a `$` would be expanded by the shell that hashes it.
    for (let i = 0; i < 20; i++) expect(randomPassword()).toMatch(/^[0-9a-f]+$/);
  });
});

describe('generated Dockerfiles build with no host state', () => {
  test('both install the published package rather than copying local files', () => {
    // The bug this fixes: the UI image needed the UI package installed ON THE HOST, so a host where
    // `bun install -g` fails turned an optional UI into a boot failure. It is a BUILD input; the
    // Dockerfile fetches it.
    for (const df of [controlDockerfile('9.9.9'), uiDockerfile('9.9.9')]) {
      expect(df).toContain('@9.9.9');           // pinned to the CLI that rendered it
      expect(df).not.toContain('COPY dist');    // nothing from the host
    }
  });

  test('the control image runs the package entry, not a global bin', () => {
    // `bun install -g` needs a writable global dir that not every host provides; when it is missing
    // the failure is an opaque "No global directory found".
    expect(controlDockerfile()).toContain('node_modules/@samyx/preview-stacks/dist/cli.js');
  });

  test('the UI image takes both the assets and the nginx config from the package', () => {
    const df = uiDockerfile();
    expect(df).toContain('/nginx.conf /etc/nginx/conf.d/default.conf');
    expect(df).toContain('/dist /usr/share/nginx/html');
  });
});

describe('init refuses a missing advanced-UI image', () => {
  test('fails by name instead of letting compose try to pull it', async () => {
    // What actually happened on a real host: the UI image was absent, compose tried to PULL
    // `pstack-ui:local`, got "pull access denied", and took the WHOLE control stack down with it —
    // Traefik included. An optional UI must not be able to kill the host.
    const runner: Runner = {
      dryRun: false,
      async run(cmd: string) {
        const missing = cmd.includes('image inspect') && cmd.includes('pstack-ui:local');
        return {
          ok: !missing,
          code: missing ? 1 : 0,
          stdout: cmd.includes('State.Health.Status') ? 'healthy\n' : '',
          stderr: '',
          skipped: false,
        };
      },
    };
    await expect(
      init({
        dataDir: `${process.env.TMPDIR ?? '/tmp'}/pstack-uipre-${process.pid}`,
        domain: 'preview.example.com',
        acmeEmail: 'o@e.com',
        challenge: 'http01',
        ui: 'advanced',
        dryRun: false,
        runner,
      }),
    ).rejects.toThrow(/advanced UI image.*build-image --ui/s);
  });

  test('the basic default never checks for it', async () => {
    const runner: Runner = {
      dryRun: false,
      async run(cmd: string) {
        // Fail the UI inspect if it is ever attempted — it must not be.
        if (cmd.includes('pstack-ui:local')) return { ok: false, code: 1, stdout: '', stderr: '', skipped: false };
        return { ok: true, code: 0, stdout: cmd.includes('State.Health.Status') ? 'healthy\n' : '', stderr: '', skipped: false };
      },
    };
    await init({
      dataDir: `${process.env.TMPDIR ?? '/tmp'}/pstack-uipre2-${process.pid}`,
      domain: 'preview.example.com',
      acmeEmail: 'o@e.com',
      challenge: 'http01',
      ui: 'basic',
      dryRun: false,
      runner,
    });
  });
});

describe('a spec source is a secret; its metadata is not', () => {
  /*
   * Reads are unauthenticated on this API by design — see the note on the route. A spec's SOURCE is
   * the one read that cannot follow that rule: hook bodies are shell strings, and a hook routinely
   * carries a credential inline. The resolved-spec view already withholds hook bodies, so serving
   * the whole file unauthenticated handed them out one route over.
   *
   * This boots the real server rather than calling a handler, because the thing under test is the
   * authorization decision on a live request — a unit test of `specs.source()` would pass either way.
   */
  const TOKEN = 'test-token-value';
  const SECRET = 'hunter2-registry-password';
  const SPEC = [
    'version: 1',
    'kind: isolated',
    'stack: web-${PR}',
    'axes:',
    '  - name: registry',
    `    up: "docker login -u ci -p ${SECRET}"`,
    '    assert_gone: "docker info >/dev/null 2>&1 || exit 1; ! docker login-check"',
    '',
  ].join('\n');

  const boot = async () => {
    const dataDir = `${process.env.TMPDIR ?? '/tmp'}/pstack-api-${process.pid}-${Math.trunc(performance.now() * 1000)}`;
    // Port 0 lets the OS pick, so a busy 7878 (a dev server on the same machine) cannot fail this.
    const server = createServer({ dataDir, port: 0, host: '127.0.0.1', token: TOKEN });
    const base = `http://127.0.0.1:${server.port}`;
    const put = await fetch(`${base}/api/specs/web`, {
      method: 'PUT',
      headers: { authorization: `Bearer ${TOKEN}`, 'content-type': 'application/json' },
      body: JSON.stringify({ spec: SPEC, description: 'the web app' }),
    });
    expect(put.status).toBe(201);
    return { server, base };
  };

  test('without a token the source is withheld — and the secret appears nowhere', async () => {
    const { server, base } = await boot();
    try {
      const r = await fetch(`${base}/api/specs/web`);
      // NOT a 401: the metadata is genuinely public, and failing the whole request would mean a
      // token is needed just to see that a spec exists.
      expect(r.status).toBe(200);
      const raw = await r.text();
      // The whole response body, not just the field: a leak anywhere in it is a leak.
      expect(raw).not.toContain(SECRET);
      expect(raw).not.toContain('docker login');

      const body = JSON.parse(raw) as Record<string, unknown>;
      expect(body.source).toBeUndefined();
      // Explicit, so a page says "needs a token" instead of rendering a blank editor that reads as
      // an empty spec.
      expect(body.sourceWithheld).toBe(true);
      // Metadata still served, including the NAMES of required variables — not credentials.
      expect(body.name).toBe('web');
      expect(body.kind).toBe('isolated');
      expect(body.description).toBe('the web app');
      expect(body.requiredVars).toEqual(['PR']);
    } finally {
      server.stop(true);
    }
  });

  test('with a token the source is served in full', async () => {
    const { server, base } = await boot();
    try {
      const r = await fetch(`${base}/api/specs/web`, {
        headers: { authorization: `Bearer ${TOKEN}` },
      });
      expect(r.status).toBe(200);
      const body = (await r.json()) as { source?: string; sourceWithheld?: boolean };
      expect(body.source).toContain(SECRET);
      expect(body.sourceWithheld).toBeUndefined();
    } finally {
      server.stop(true);
    }
  });

  test('a wrong token is treated as no token, not as an error', async () => {
    // Same shape as the unauthenticated case: metadata, no source. A 401 here would let someone
    // probe token validity against a route that is otherwise public.
    const { server, base } = await boot();
    try {
      const r = await fetch(`${base}/api/specs/web`, { headers: { authorization: 'Bearer wrong' } });
      expect(r.status).toBe(200);
      expect(await r.text()).not.toContain(SECRET);
    } finally {
      server.stop(true);
    }
  });

  test('the LIST route never carried a source to begin with', async () => {
    const { server, base } = await boot();
    try {
      const raw = await (await fetch(`${base}/api/specs`)).text();
      expect(raw).not.toContain(SECRET);
      expect(JSON.parse(raw).specs).toHaveLength(1);
    } finally {
      server.stop(true);
    }
  });
});

describe('wildcard subdomains — routing a whole subtree at one profile', () => {
  /*
   * The rule is a Go regexp Traefik matches every request against, so "roughly right" is not a
   * category: too loose routes someone else's hostname into this PR, too tight silently never fires.
   * These tests exercise it as a regexp rather than string-comparing the output.
   */
  const HOST = 'backend-pr-123.preview.example.com';

  /** Extract the pattern from `HostRegexp(`…`)` and match it the way Traefik would. */
  const re = (rule: string): RegExp => {
    const m = /^HostRegexp\(`(.+)`\)$/.exec(rule);
    expect(m).not.toBeNull();
    return new RegExp(m![1]!);
  };

  test('depth "one" matches exactly one extra label', () => {
    const r = re(wildcardRule(HOST, 'one'));
    expect(r.test(`api.${HOST}`)).toBe(true);
    expect(r.test(`tenant-a.${HOST}`)).toBe(true);
    // Deeper must NOT match: DNS and TLS cannot cover it, so routing it would produce a host that
    // resolves, routes, and then fails the handshake — the worst of the three outcomes to debug.
    expect(r.test(`a.b.${HOST}`)).toBe(false);
    // And the bare host is left to the exact-host router that already owns it.
    expect(r.test(HOST)).toBe(false);
  });

  test('depth "any" matches any depth', () => {
    const r = re(wildcardRule(HOST, 'any'));
    expect(r.test(`api.${HOST}`)).toBe(true);
    expect(r.test(`a.b.${HOST}`)).toBe(true);
    expect(r.test(`a.b.c.d.${HOST}`)).toBe(true);
    expect(r.test(HOST)).toBe(false);
  });

  test('the host is regexp-escaped, so a dot cannot act as a wildcard', () => {
    const r = re(wildcardRule(HOST, 'one'));
    // The bug an unescaped `.` produces: someone registers `backend-pr-123Xpreview.example.com`
    // and Traefik hands them this PR's backend.
    expect(r.test('api.backend-pr-123Xpreview.example.com')).toBe(false);
    expect(wildcardRule(HOST, 'one')).toContain('backend-pr-123\\.preview\\.example\\.com');
  });

  test('it is anchored at both ends', () => {
    const r = re(wildcardRule(HOST, 'one'));
    // Unanchored, this would match — a suffix attack in the first case, a prefix one in the second.
    expect(r.test(`api.${HOST}.evil.test`)).toBe(false);
    expect(r.test(`prefix-api.${HOST}`)).toBe(true); // a legitimate single label
    expect(r.test(`api/${HOST}`)).toBe(false);
  });

  test('a profile name becomes a usable env var name', () => {
    expect(subdomainVarName('backend')).toBe('PSTACK_WILD_BACKEND');
    // A dash cannot appear in an environment variable name.
    expect(subdomainVarName('admin-api')).toBe('PSTACK_WILD_ADMIN_API');
  });

  describe('spec parsing', () => {
    const withSubs = (subs: string, extra = '') => `
version: 1
stack: pr-\${PR}
env:
  DOMAIN: preview.example.com
compose:
  file: dc.yml
  profiles: [backend, frontend]
  subdomains: ${subs}
${extra}axes: []
`;

    test('the short list form derives the host from profile and stack', () => {
      const s = spec(withSubs('[backend]'));
      expect(s.compose!.subdomains).toEqual([
        {
          profile: 'backend',
          host: 'backend-pr-7.preview.example.com',
          depth: 'one',
          varName: 'PSTACK_WILD_BACKEND',
          rule: wildcardRule('backend-pr-7.preview.example.com', 'one'),
        },
      ]);
    });

    test('the mapping form takes a depth, and an explicit host may interpolate', () => {
      const s = spec(`
version: 1
stack: pr-\${PR}
env:
  DOMAIN: preview.example.com
compose:
  file: dc.yml
  profiles: [backend, frontend]
  subdomains:
    backend: any
    frontend:
      host: app-\${STACK}.\${DOMAIN}
axes: []
`);
      const [backend, frontend] = s.compose!.subdomains!;
      expect(backend!.depth).toBe('any');
      // ${STACK} resolves inside an explicit host — it is interpolated with everything else.
      expect(frontend!.host).toBe('app-pr-7.preview.example.com');
      expect(frontend!.depth).toBe('one');
    });

    test('a subdomain for a profile that is never started is refused', () => {
      // Dead config, and the likeliest cause is a typo in one of the two lists.
      expect(() => spec(withSubs('[nope]'))).toThrow(/not in compose.profiles/);
    });

    test('no DOMAIN is a hard error, not a rule that matches nothing', () => {
      expect(() =>
        spec('version: 1\nstack: s\ncompose:\n  file: dc.yml\n  profiles: [backend]\n  subdomains: [backend]\naxes: []', {}),
      ).toThrow(/needs a domain/);
    });

    test('two profiles colliding on one env var name is refused', () => {
      // `admin-api` and `admin_api` both become PSTACK_WILD_ADMIN_API, so one rule would silently
      // overwrite the other and that profile would route nothing.
      expect(() =>
        spec(`
version: 1
stack: s
env:
  DOMAIN: preview.example.com
compose:
  file: dc.yml
  profiles: [admin-api, admin_api]
  subdomains: [admin-api, admin_api]
axes: []
`),
      ).toThrow(/both map to PSTACK_WILD_ADMIN_API/);
    });

    test('an unknown depth names the two valid ones', () => {
      expect(() => spec(withSubs('{ backend: deep }'))).toThrow(/must be "one".*or "any"/s);
    });

    test('subdomains are absent from the spec unless asked for', () => {
      const s = spec('version: 1\nstack: s\ncompose:\n  file: dc.yml\n  profiles: [backend]\naxes: []');
      expect(s.compose!.subdomains).toBeUndefined();
    });
  });

  describe('compose invocation', () => {
    /** Capture the env a compose subcommand runs with. */
    const envRunner = () => {
      const seen: Array<Record<string, string | undefined>> = [];
      return {
        seen,
        runner: {
          dryRun: false,
          async run(_cmd: string, opts?: { env?: Record<string, string | undefined> }) {
            seen.push(opts?.env ?? {});
            return { ok: true, code: 0, stdout: '', stderr: '', skipped: false };
          },
        } as Runner,
      };
    };

    const s = () =>
      spec(`
version: 1
stack: pr-\${PR}
env:
  DOMAIN: preview.example.com
compose:
  file: dc.yml
  profiles: [backend]
  subdomains: [backend]
axes: []
`);

    test('up exports the rule for a label to interpolate', async () => {
      const { seen, runner } = envRunner();
      await composeUp(s(), runner, {});
      expect(seen[0]!.PSTACK_WILD_BACKEND).toBe(
        wildcardRule('backend-pr-7.preview.example.com', 'one'),
      );
    });

    test('DOWN exports it too — the reason this is not only on `up`', async () => {
      // Compose interpolates the compose FILE on every subcommand. Without the variable here, a
      // label referencing it substitutes empty, so `down` reasons about a differently-labelled
      // project than `up` created.
      const { seen, runner } = envRunner();
      await composeDown(s(), runner);
      expect(seen[0]!.PSTACK_WILD_BACKEND).toBeDefined();
    });
  });
});

describe("Traefik dynamic config — one bad write must not take down other people's routes", () => {
  /*
   * Traefik's documented behaviour: an unparseable file in the watched directory is a parse error for
   * the DIRECTORY, and the rest of it can be discarded with it. So the failure mode is not "my new
   * middleware is broken", it is routes elsewhere vanishing. Everything below defends that.
   */
  const tmp = () =>
    `${process.env.TMPDIR ?? '/tmp'}/pstack-routing-${process.pid}-${Math.trunc(performance.now() * 1000)}`;

  const store = async (): Promise<RoutingStore> => {
    const dir = tmp();
    await Bun.write(`${dir}/.keep`, '');
    return new RoutingStore(dir);
  };

  const VALID = 'http:\n  middlewares:\n    dashboard-auth:\n      basicAuth:\n        users:\n          - "admin:$apr1$x"\n';

  describe('validation happens before anything touches disk', () => {
    test('unparseable YAML is refused', () => {
      expect(() => validateRoutingContent('http:\n  - [unclosed')).toThrow(RoutingError);
      expect(() => validateRoutingContent('http:\n  - [unclosed')).toThrow(/not valid YAML/);
    });

    test('a typo in a top-level section is refused, not silently ignored', () => {
      // The insidious case: `htttp:` is perfectly good YAML, so Traefik loads the file and applies
      // nothing. Without this check the symptom is "I added the middleware and nothing happened".
      expect(() => validateRoutingContent('htttp:\n  middlewares: {}')).toThrow(/unknown top-level/);
      // Same shape: middlewares belong UNDER http, and at the top level they do nothing at all.
      expect(() => validateRoutingContent('middlewares:\n  a:\n    basicAuth: {}')).toThrow(
        /unknown top-level/,
      );
    });

    test('empty, non-mapping and section-less content are all refused', () => {
      expect(() => validateRoutingContent('   ')).toThrow(/empty/);
      expect(() => validateRoutingContent('- a\n- b')).toThrow(/must be a mapping/);
      expect(() => validateRoutingContent('{}')).toThrow(/no sections/);
    });

    test('every real Traefik section is accepted', () => {
      for (const s of ['http', 'tcp', 'udp', 'tls']) {
        expect(() => validateRoutingContent(`${s}:\n  routers: {}`)).not.toThrow();
      }
    });
  });

  describe('filenames', () => {
    test('traversal and separators are refused', async () => {
      const s = await store();
      for (const bad of ['../evil.yml', 'a/b.yml', '..%2Fx.yml', '.hidden.yml']) {
        await expect(s.write(bad, VALID)).rejects.toThrow(/invalid filename/);
      }
    });

    test('a non-YAML extension is refused — Traefik would not read it anyway', async () => {
      const s = await store();
      await expect(s.write('middleware.txt', VALID)).rejects.toThrow(/invalid filename/);
      await expect(s.write('middleware', VALID)).rejects.toThrow(/invalid filename/);
    });

    test('.yml and .yaml both work', async () => {
      const s = await store();
      await s.write('a.yml', VALID);
      await s.write('b.yaml', VALID);
      expect((await s.list()).map((f) => f.name)).toEqual(['a.yml', 'b.yaml']);
    });
  });

  describe('writing', () => {
    test('a rejected file is never created', async () => {
      const s = await store();
      await expect(s.write('bad.yml', 'htttp: {}')).rejects.toThrow(RoutingError);
      expect(await s.list()).toEqual([]);
    });

    test('write returns the previous content, so a caller can offer an undo', async () => {
      // There is deliberately NO on-disk history: the obvious place to keep it is the one directory
      // that must contain nothing but dynamic config.
      const s = await store();
      expect(await s.write('m.yml', VALID)).toBeNull(); // new
      const next = 'http:\n  routers: {}\n';
      expect(await s.write('m.yml', next)).toBe(VALID); // replaced
      expect(await s.read('m.yml')).toBe(next);
    });

    test('it leaves no temp file behind — anything in that directory gets parsed', async () => {
      const s = await store();
      await s.write('m.yml', VALID);
      const { readdir } = await import('node:fs/promises');
      const entries = await readdir(s.dir);
      expect(entries.filter((e) => e.includes('pstack-tmp'))).toEqual([]);
    });

    test('list only reports files Traefik would actually read', async () => {
      const s = await store();
      await s.write('good.yml', VALID);
      await Bun.write(`${s.dir}/notes.txt`, 'ignore me');
      await Bun.write(`${s.dir}/.hidden.yml`, VALID);
      expect((await s.list()).map((f) => f.name)).toEqual(['good.yml']);
    });

    test('a missing directory reads as not-writable rather than throwing', async () => {
      // The pre-0.4.0 control stack does not mount this into the API at all, and the answer has to be
      // "re-run pstack init", not an ENOENT on every request.
      const s = new RoutingStore(`${tmp()}/definitely-absent`);
      expect(await s.writable()).toBe(false);
      expect(await s.list()).toEqual([]);
      await expect(s.write('m.yml', VALID)).rejects.toThrow(/re-run `pstack init`/);
    });
  });

  test('delete returns what it removed', async () => {
    const s = await store();
    await s.write('m.yml', VALID);
    expect(await s.remove('m.yml')).toBe(VALID);
    expect(await s.list()).toEqual([]);
    await expect(s.remove('m.yml')).rejects.toThrow(/no such routing file/);
  });

  describe('over HTTP', () => {
    const TOKEN = 'routing-token-value';
    const boot = async () => {
      const dataDir = tmp();
      const routingDir = `${dataDir}-dynamic`;
      await Bun.write(`${routingDir}/.keep`, '');
      const server = createServer({ dataDir, port: 0, host: '127.0.0.1', token: TOKEN, routingDir });
      return { server, base: `http://127.0.0.1:${server.port}`, routingDir };
    };

    test('content needs a token; the file list does not', async () => {
      const { server, base } = await boot();
      try {
        const put = await fetch(`${base}/api/routing/auth.yml`, {
          method: 'PUT',
          headers: { authorization: `Bearer ${TOKEN}`, 'content-type': 'application/json' },
          body: JSON.stringify({ content: VALID }),
        });
        expect(put.status).toBe(201);

        // The list renders without a token — it is filenames, not configuration.
        const list = (await (await fetch(`${base}/api/routing`)).json()) as {
          files: Array<{ name: string }>;
          writable: boolean;
        };
        expect(list.files.map((f) => f.name)).toEqual(['auth.yml']);
        expect(list.writable).toBe(true);

        // The CONTENT does not: dynamic config holds basicAuth hashes and forwardAuth URLs.
        const raw = await (await fetch(`${base}/api/routing/auth.yml`)).text();
        expect(raw).not.toContain('apr1');
        expect(JSON.parse(raw).sourceWithheld).toBe(true);

        const authedRead = (await (
          await fetch(`${base}/api/routing/auth.yml`, { headers: { authorization: `Bearer ${TOKEN}` } })
        ).json()) as { content: string };
        expect(authedRead.content).toBe(VALID);
      } finally {
        server.stop(true);
      }
    });

    test('a write without a token is refused, and changes nothing', async () => {
      const { server, base, routingDir } = await boot();
      try {
        const r = await fetch(`${base}/api/routing/nope.yml`, {
          method: 'PUT',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ content: VALID }),
        });
        expect(r.status).toBe(401);
        const { readdir } = await import('node:fs/promises');
        expect((await readdir(routingDir)).filter((f) => f.endsWith('.yml'))).toEqual([]);
      } finally {
        server.stop(true);
      }
    });

    test('a rejected file answers 400 with the reason, not 500', async () => {
      const { server, base } = await boot();
      try {
        const r = await fetch(`${base}/api/routing/bad.yml`, {
          method: 'PUT',
          headers: { authorization: `Bearer ${TOKEN}`, 'content-type': 'application/json' },
          body: JSON.stringify({ content: 'htttp: {}' }),
        });
        expect(r.status).toBe(400);
        expect(((await r.json()) as { error: string }).error).toMatch(/unknown top-level/);
      } finally {
        server.stop(true);
      }
    });
  });
});

describe("a deployment's stored source — so replacing is editing, not retyping", () => {
  const TOKEN = 'src-token-value';
  const SECRET = 'deploy-hook-secret-value';
  const SPEC = ['version: 1', 'kind: shared', 'stack: shared-ingress', 'axes: []', ''].join('\n');
  const COMPOSE = [
    'services:',
    '  postgres:',
    '    image: postgres:17',
    '    environment:',
    `      POSTGRES_PASSWORD: ${SECRET}`,
    '',
  ].join('\n');

  const boot = async () => {
    const dataDir = `${process.env.TMPDIR ?? '/tmp'}/pstack-src-${process.pid}-${Math.trunc(performance.now() * 1000)}`;
    const server = createServer({ dataDir, port: 0, host: '127.0.0.1', token: TOKEN });
    const base = `http://127.0.0.1:${server.port}`;
    const put = await fetch(`${base}/api/deployments/ingress`, {
      method: 'PUT',
      headers: { authorization: `Bearer ${TOKEN}`, 'content-type': 'application/json' },
      body: JSON.stringify({ spec: SPEC, compose: COMPOSE }),
    });
    expect(put.status).toBe(201);
    return { server, base };
  };

  test('with a token it returns the spec and the compose file verbatim', async () => {
    // The point: the replace form can pre-fill. Retyping a spec from memory drops whatever you
    // forget, and a dropped axis stops being tracked while what it created keeps running.
    const { server, base } = await boot();
    try {
      const r = await fetch(`${base}/api/deployments/ingress/source`, {
        headers: { authorization: `Bearer ${TOKEN}` },
      });
      expect(r.status).toBe(200);
      const body = (await r.json()) as { spec: string; compose: string | null; specName: string | null };
      expect(body.spec).toBe(SPEC);
      expect(body.compose).toBe(COMPOSE);
      expect(body.specName).toBeNull();
    } finally {
      server.stop(true);
    }
  });

  test('without a token it is withheld, and the hook secret does not leak', async () => {
    const { server, base } = await boot();
    try {
      const raw = await (await fetch(`${base}/api/deployments/ingress/source`)).text();
      expect(raw).not.toContain(SECRET);
      const body = JSON.parse(raw);
      expect(body.sourceWithheld).toBe(true);
      expect(body.spec).toBeUndefined();
    } finally {
      server.stop(true);
    }
  });

  test('an unknown deployment is a 404', async () => {
    const { server, base } = await boot();
    try {
      const r = await fetch(`${base}/api/deployments/nope/source`, {
        headers: { authorization: `Bearer ${TOKEN}` },
      });
      expect(r.status).toBe(404);
    } finally {
      server.stop(true);
    }
  });
});
