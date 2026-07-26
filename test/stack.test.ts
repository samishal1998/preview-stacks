import { describe, expect, test } from 'bun:test';
import { parseSpec, SpecError, interpolate, warnings } from '../src/spec.ts';
import { captureOutputs, createRunner, type RunResult, type Runner } from '../src/exec.ts';
import { down, up, verify } from '../src/stack.ts';
import { nullSink } from '../src/log.ts';
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
