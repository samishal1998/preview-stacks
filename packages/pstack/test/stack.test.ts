import { describe, expect, test } from 'bun:test';
import { parseSpec, SpecError, interpolate, warnings } from '../src/spec.ts';
import { captureOutputs, createRunner, type RunResult, type Runner } from '../src/exec.ts';
import { down, up, verify } from '../src/stack.ts';
import { nullSink } from '../src/log.ts';
import { init } from '../src/init.ts';
import { buildImage } from '../src/image.ts';
import { SpecStore, findRequiredVars } from '../src/specs.ts';
import { renderCloudInit, randomPassword } from '../src/cloudinit.ts';
import { Registry } from '../src/registry.ts';
import { displayDeclared, isSecretName, mask, redactText } from '../src/redact.ts';
import { shq } from '../src/compose.ts';

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
    // The one mistake with no cheap recovery: a booted host you cannot log into.
    expect(() => renderCloudInit({ ...base, sshKey: 'my-key' })).toThrow(/authorized_keys/);
    expect(() => renderCloudInit({ ...base, challenge: 'dns01' })).toThrow(/provider/);
    // Interpolated into a single-quoted shell command.
    expect(() => renderCloudInit({ ...base, dashboardPassword: "a'b" })).toThrow(/single quote/);
  });

  test('generated passwords are shell-safe', () => {
    // Hex on purpose: a `$` would be expanded by the shell that hashes it.
    for (let i = 0; i < 20; i++) expect(randomPassword()).toMatch(/^[0-9a-f]+$/);
  });
});
