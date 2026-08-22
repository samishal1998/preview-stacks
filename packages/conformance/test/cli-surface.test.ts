/**
 * The CLI surface: version, healthcheck, unknown commands — and the `serve` tuning knobs, graded
 * through the real `serve` (ported from packages/pstack/test/stack.test.ts, 'the CLI surface').
 */
import { describe, expect, test } from 'bun:test';
import { NO_CLI, REPO } from '../harness/impl.ts';
import { bootServer, tmpd, waitJob } from '../harness/server.ts';
import { runCli } from '../harness/cli.ts';
import { arm, dockerShim } from '../harness/docker-shim.ts';

/** The version the CLI must agree with — read, not imported: nothing here touches src. */
const pkgJson = (await Bun.file(`${REPO}/packages/pstack/package.json`).json()) as { version: string };

describe.skipIf(NO_CLI)('the CLI surface: version, and unknown commands', () => {
  const run = (args: string[]) => runCli(args, { env: { PSTACK_DATA: tmpd('cli') } });
  const runWith = (args: string[], env: Record<string, string>) => runCli(args, { env });

  test('healthcheck answers 0 against a live server and 1 against nothing — the container HEALTHCHECK', async () => {
    // Three Dockerfiles used to carry a `bun --eval fetch(...)` one-liner each. One command the
    // binary owns means one implementation, no runtime assumption in the image, and a Go build
    // can ship the same HEALTHCHECK line. `init` blocks on this verdict, so it has to be boring.
    // negative control: make the command exit 0 unconditionally — the dead-port case fails.
    const s = await bootServer({ tag: 'hc' });
    try {
      const live = await runWith(['healthcheck'], { PSTACK_PORT: String(s.port) });
      expect(live.code).toBe(0);
    } finally {
      await s.stop();
    }
    // A port nothing listens on: refused at once, not hung until docker's timeout.
    const dead = await runWith(['healthcheck'], { PSTACK_PORT: '1' });
    expect(dead.code).toBe(1);
    // It is a real command: listed, so the unknown-command gate and --help agree about it.
    const help = await run(['--help']);
    expect(help.stdout).toContain('healthcheck');
  }, 20_000);

  test('--version prints the version alone, so a script can compare it', async () => {
    const r = await run(['--version']);
    expect(r.code).toBe(0);
    // Bare — no prefix, no banner: `[ "$(pstack --version)" = 0.25.1 ] || pstack upgrade`.
    expect(r.stdout.trim()).toBe(pkgJson.version);
    expect((await run(['-V'])).stdout.trim()).toBe(pkgJson.version);
  }, 20_000);

  test('an unknown command says so — it does not go looking for a spec file', async () => {
    /*
     * The reported confusion. Spec-loading used to be gated by an allowlist of spec-FREE commands,
     * so anything NOT on it fell into the loader: a typo, and every command added later, failed with
     * `spec not found: preview.yml`. That is what `pstack upgrade` printed on a host still running
     * the version before `upgrade` existed — an error naming a file the operator never mentioned.
     */
    const r = await run(['upgradee']);
    expect(r.code).toBe(3);
    expect(r.all).toContain('unknown command "upgradee"');
    // It names the version, because "this host is older than you think" is the usual cause.
    expect(r.all).toContain(pkgJson.version);
    expect(r.all).not.toContain('preview.yml');
  }, 20_000);

  test('a spec command still reports a missing spec, and upgrade never asks for one', async () => {
    // The other half: the spec error is right for a command that reads a spec.
    const spec = await run(['status']);
    expect(spec.all).toContain('preview.yml');

    // `upgrade` reads the HOST, not a spec — so with no control stack it says THAT.
    const up = await run(['upgrade', '-n']);
    expect(up.all).toContain('no control stack found');
    expect(up.all).not.toContain('preview.yml');
  }, 20_000);
});

describe('serve reads its tuning knobs from the environment', () => {
  test('serve reads its tuning knobs from the environment with ?? semantics', async () => {
    // What a black-box harness needs to reach the readiness-timeout and SSO-expiry paths through
    // the real `serve`, not through createServer options it cannot see. Proven from the outside:
    // a healthcheck that never settles reaches `timedout` in well under a second — which the
    // default 180s deadline could never do, so the knob was read, not ignored.
    // negative control: ignore PSTACK_READINESS_TIMEOUT_MS — the read below sleeps its full 5s
    // and comes back `watching`, not `timedout`.
    const shim = dockerShim(
      [
        arm('ps -aq *', 'c1'),
        arm(
          'inspect *',
          '[{"Id":"c1","Name":"/probe-app-1","RestartCount":0,"Config":{"Image":"nginx","Labels":{"com.docker.compose.service":"app","com.docker.compose.project":"probe"}},"State":{"Status":"running","ExitCode":0,"Health":{"Status":"starting"}},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"172.20.0.5"}},"Ports":{}}}]',
        ),
      ].join('\n'),
    );
    const s = await bootServer({ tag: 'knobs', pathPrefix: shim.dir, readiness: { pollMs: 20, timeoutMs: 300 } });
    try {
      await fetch(`${s.base}/api/deployments/d`, {
        method: 'PUT',
        headers: s.H,
        body: JSON.stringify({ spec: 'version: 1\nstack: probe\naxes:\n  - name: a\n    up: "true"\n' }),
      });
      // The deploy is what a production watch hangs off — a read-started watch is deliberately silent.
      const { job } = (await (await fetch(`${s.base}/api/deployments/d/up`, { method: 'POST', headers: s.H })).json()) as { job: { id: string } };
      await waitJob(s, job.id);

      const started = Date.now();
      const snap = (await (await fetch(`${s.base}/api/deployments/d/readiness?wait=5`, { headers: s.H })).json()) as { state: string };
      expect(snap.state).toBe('timedout');
      expect(Date.now() - started).toBeLessThan(1_000);
    } finally {
      await s.stop();
      shim.remove();
    }
  }, 20_000);
});
