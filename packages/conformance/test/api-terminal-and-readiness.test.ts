/**
 * The container terminal (the most dangerous route in the product) and readiness — did the stack
 * actually come up. Ported from packages/pstack/test/stack.test.ts, black-box: a spawned server, a
 * fake `docker` on its PATH, and the event bus observed through a `*` webhook notifier.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer, type Booted } from '../harness/server.ts';
import { arm, dockerShim, type Shim } from '../harness/docker-shim.ts';
import { eventTap } from '../harness/receiver.ts';

type Tap = Awaited<ReturnType<typeof eventTap>>;
type Got = { event: string; data: Record<string, unknown> };
const gotOf = (tap: Tap): Got[] => tap.got.map((d) => ({ event: d.event, data: d.body?.data ?? {} }));
/** Block until the tap has delivered an event matching `m` (or `ms` elapsed). */
const untilEvent = async (tap: Tap, m: (name: string) => boolean, ms = 5000): Promise<void> => {
  const deadline = Date.now() + ms;
  while (!tap.events().some(m) && Date.now() < deadline) await Bun.sleep(10);
};

describe('container terminal', () => {
  /**
   * A fake `docker` on PATH, so `deploymentRuntime` reports a container without a Docker daemon.
   * The alternative — asserting the guard against an empty container list — proves nothing: an
   * endpoint that refuses everything passes it. This makes the ALLOW path real, so the refusals
   * below are refusals of something that would otherwise have worked.
   *
   * A real local `sh` stands in for `docker exec -i` — the socket plumbing is then PROVEN rather
   * than asserted about a command this machine cannot run.
   */
  const shim = () =>
    dockerShim(
      [
        arm('"ps -aq "*', 'c0ffee123456'),
        arm(
          '"inspect "*',
          '[{"Id":"c0ffee123456","Name":"/probe-app-1","Config":{"Image":"nginx","Labels":{"com.docker.compose.service":"app","com.docker.compose.project":"probe"}},"State":{"Status":"running"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"172.20.0.5"}},"Ports":{}}}]',
        ),
        '  "exec -i "*) exec sh ;;',
      ].join('\n'),
    );

  const bootTerm = async () => {
    const docker = shim();
    const s = await bootServer({ tag: 'term', pathPrefix: docker.dir });
    return { s, docker };
  };
  const putSpec = (s: Booted) =>
    fetch(`${s.base}/api/deployments/d`, {
      method: 'PUT',
      headers: s.H,
      body: JSON.stringify({ spec: 'version: 1\nstack: probe\naxes:\n  - name: a\n    up: "true"\n' }),
    });

  test('a container the deployment does not own is refused — this is the host escape', async () => {
    // `docker exec` accepts ANY container on the daemon: Traefik, another PR's stack, and the pstack
    // control container itself, whose filesystem is pstack.db — every password hash and every
    // notifier signing secret. The name is matched against what this deployment owns, or it is a 404.
    const { s, docker } = await bootTerm();
    try {
      await putSpec(s);
      for (const name of ['pstack-control', 'traefik', 'c0ffee999999', '../../etc/passwd']) {
        const r = await fetch(`${s.base}/api/deployments/d/terminal?container=${encodeURIComponent(name)}`, {
          headers: s.H,
        });
        expect(`${name} → ${r.status}`).toBe(`${name} → 404`);
      }
      // …and the one it DOES own upgrades, so the refusals above are refusing something real.
      const ok = await fetch(`${s.base}/api/deployments/d/terminal?container=probe-app-1`, {
        headers: { ...s.H, connection: 'Upgrade', upgrade: 'websocket', 'sec-websocket-version': '13', 'sec-websocket-key': 'dGhlIHNhbXBsZSBub25jZQ==' },
      });
      expect(ok.status).toBe(101);
    } finally {
      await s.stop();
      docker.remove();
    }
  });

  test('unauthenticated, and a shell outside the allowlist, never reach docker', async () => {
    const { s, docker } = await bootTerm();
    try {
      await putSpec(s);
      expect((await fetch(`${s.base}/api/deployments/d/terminal?container=probe-app-1`)).status).toBe(401);
      const bad = await fetch(`${s.base}/api/deployments/d/terminal?container=probe-app-1&shell=evil`, { headers: s.H });
      expect(bad.status).toBe(400);
      expect(((await bad.json()) as { error: string }).error).toMatch(/shell must be one of/);
      expect((await fetch(`${s.base}/api/deployments/d/terminal`, { headers: s.H })).status).toBe(400);
      expect((await fetch(`${s.base}/api/deployments/nope/terminal?container=x`, { headers: s.H })).status).toBe(404);
    } finally {
      await s.stop();
      docker.remove();
    }
  });

  test('keystrokes reach the shell, output and exit come back, and the session is audited', async () => {
    const { s, docker } = await bootTerm();
    try {
      await putSpec(s);
      const frames: Array<{ binary: boolean; text: string }> = [];
      const ws = new WebSocket(
        `ws://127.0.0.1:${s.port}/api/deployments/d/terminal?container=probe-app-1`,
        { headers: { authorization: `Bearer ${s.token}` } } as unknown as string[],
      );
      ws.binaryType = 'arraybuffer';
      ws.onmessage = (e) =>
        frames.push(
          typeof e.data === 'string'
            ? { binary: false, text: e.data }
            : { binary: true, text: new TextDecoder().decode(e.data as ArrayBuffer) },
        );
      await new Promise<void>((r) => {
        ws.onopen = () => r();
        ws.onerror = () => r();
      });
      // `\r`, NOT `\n` — this is what a terminal emulator actually sends for Enter, and with no pty
      // there is no line discipline to convert it. Written with `\n` this test passed while the
      // browser sat there looking dead: every keystroke arrived and nothing ever ran.
      ws.send('echo MARKER-OUT; echo MARKER-ERR 1>&2; exit 7\r');
      for (let i = 0; i < 40 && !frames.map((f) => f.text).join('').includes('exited'); i++) await Bun.sleep(50);

      const text = frames.map((f) => f.text).join('');
      expect(text).toContain('MARKER-OUT'); // stdout
      expect(text).toContain('MARKER-ERR'); // stderr is pumped too, or half the failures are invisible
      expect(text).toContain('shell exited (7)'); // the exit CODE, not just "closed"
      expect(text).toContain('no TTY'); // the ceiling is stated to the operator, not left to guess
      // The banner is a TEXT frame and comes first; the shell's bytes are BINARY frames — decoding
      // them to a string server-side would corrupt a multi-byte character split across two reads.
      expect(frames[0]!.binary).toBe(false);
      expect(frames[0]!.text).toContain('no TTY');
      expect(frames.find((f) => f.text.includes('MARKER-OUT'))!.binary).toBe(true);
      expect(frames.find((f) => f.text.includes('MARKER-ERR'))!.binary).toBe(true);

      const { sessions } = (await (await fetch(`${s.base}/api/terminal-sessions`, { headers: s.H })).json()) as {
        sessions: Array<{ actor: string; container: string; deployment: string; endedAt: number | null }>;
      };
      expect(sessions).toHaveLength(1);
      expect(sessions[0]!.container).toBe('probe-app-1');
      expect(sessions[0]!.deployment).toBe('d');
      expect(sessions[0]!.actor).toBe('root (PSTACK_TOKEN)');
      expect(sessions[0]!.endedAt).not.toBeNull();
    } finally {
      await s.stop();
      docker.remove();
    }
  });
});

describe('readiness: did the stack actually come up', () => {
  /**
   * A `docker` on PATH whose inspect output the test REWRITES between polls — health transitions and
   * crash loops are the whole subject here, and a fixed shim could only ever prove the first
   * observation.
   */
  const mutableDocker = () => {
    const arms = (ids: string, over: { state?: string; health?: string; exit?: number; restarts?: number } = {}) =>
      [
        arm('"ps -aq "*', ids),
        arm(
          '"inspect "*',
          JSON.stringify([
            {
              Id: 'c1',
              Name: '/probe-app-1',
              RestartCount: over.restarts ?? 0,
              Config: {
                Image: 'nginx',
                Labels: { 'com.docker.compose.service': 'app', 'com.docker.compose.project': 'probe' },
              },
              State: {
                Status: over.state ?? 'running',
                ExitCode: over.exit ?? 0,
                ...(over.health ? { Health: { Status: over.health } } : {}),
              },
              NetworkSettings: {
                Networks: { 'preview-ingress': { IPAddress: '172.20.0.5' } },
                Ports: {},
              },
            },
          ]),
        ),
      ].join('\n');
    const shim: Shim = dockerShim(arms('c1'));
    const set = (over: { state?: string; health?: string; exit?: number; restarts?: number } = {}) => shim.rewrite(arms('c1', over));
    /** No containers at all — a deployment that was submitted and never deployed. */
    const empty = () => shim.rewrite(arms(''));
    return { dir: shim.dir, set, empty, restore: shim.remove };
  };

  const boot = (docker: { dir: string }, readiness: { pollMs?: number; timeoutMs?: number } = {}) =>
    bootServer({ tag: 'ready', pathPrefix: docker.dir, readiness: { pollMs: 20, timeoutMs: 5_000, ...readiness } });

  const putSpec = (s: Booted) =>
    fetch(`${s.base}/api/deployments/d`, {
      method: 'PUT',
      headers: s.H,
      body: JSON.stringify({ spec: 'version: 1\nstack: probe\naxes:\n  - name: a\n    up: "true"\n' }),
    });

  /**
   * Submit and DEPLOY, then wait for the job to finish.
   *
   * The deploy is what a production watch hangs off, and it is not incidental to these tests: a
   * watch started by a mere read is deliberately silent, so asserting events against a
   * read-started watch would assert nothing. Waiting for the job to be terminal removes the race —
   * `readiness.start` runs inside the job's work function, so once the job has ended the watch
   * exists.
   */
  const deploy = async (s: Booted): Promise<void> => {
    await putSpec(s);
    const r = await fetch(`${s.base}/api/deployments/d/up`, { method: 'POST', headers: s.H });
    const { job } = (await r.json()) as { job: { id: string } };
    for (let i = 0; i < 200; i++) {
      const j = (await (await fetch(`${s.base}/api/jobs/${job.id}`, { headers: s.H })).json()) as {
        job: { state: string };
      };
      if (j.job.state !== 'running') return;
      await Bun.sleep(10);
    }
    throw new Error('the deploy job never finished');
  };

  /** Collect domain events for the duration of one test — through a `*` webhook, as any receiver would. */
  const capture = async (s: Booted) => {
    const tap = await eventTap(s.base, s.H);
    return {
      get got() {
        return gotOf(tap);
      },
      names: () => tap.events(),
      /** Deliveries lag emission by a round trip; wait for the one the assertion is about. */
      until: (m: (name: string) => boolean, ms?: number) => untilEvent(tap, m, ms),
      off: () => tap.stop(),
    };
  };

  type Snap = {
    state: string;
    containers: Array<{
      name: string;
      ready: boolean;
      failed: boolean;
      hasHealthcheck: boolean;
      restartCount: number;
      reason?: string;
    }>;
  };
  const read = async (s: Booted, q = '?wait=5') =>
    (await (await fetch(`${s.base}/api/deployments/d/readiness${q}`, { headers: s.H })).json()) as Snap;

  test('a container with no healthcheck is ready when it runs — and says nobody probed it', async () => {
    const docker = mutableDocker();
    const s = await boot(docker);
    const cap = await capture(s);
    try {
      await deploy(s);
      const started = Date.now();
      const snap = await read(s);

      expect(snap.state).toBe('ready');
      expect(snap.containers).toHaveLength(1);
      // The honest ceiling: ready, but nothing checked that it SERVES anything.
      expect(snap.containers[0]!.hasHealthcheck).toBe(false);
      await cap.until((n) => n === 'stack.ready');
      expect(cap.names()).toContain('container.ready');
      expect(cap.names()).toContain('stack.ready');
      // A `wait` that returns as soon as the answer exists is the point of the long poll — if it
      // slept the full 5s this would pass on the timeout instead of on convergence.
      expect(Date.now() - started).toBeLessThan(3_000);
    } finally {
      cap.off();
      await s.stop();
      docker.restore();
    }
  }, 20_000);

  test('a healthcheck is narrated: started, updated, finished — then the container is ready', async () => {
    const docker = mutableDocker();
    docker.set({ health: 'starting' });
    const s = await boot(docker);
    const cap = await capture(s);
    // Flip on the FIRST observation rather than on a timer: a race here would make the test pass or
    // fail on scheduling, and `starting → healthy` is exactly the transition being asserted.
    const flip = cap.until((n) => n === 'healthcheck.started', 10_000).then(() => docker.set({ health: 'healthy' }));
    try {
      await deploy(s);
      const snap = await read(s);
      expect(snap.state).toBe('ready');
      await cap.until((n) => n === 'stack.ready');

      const hc = cap.got.filter((g) => g.event.startsWith('healthcheck.'));
      expect(hc.map((g) => g.event)).toEqual([
        'healthcheck.started',
        'healthcheck.updated',
        'healthcheck.finished',
      ]);
      expect(hc[0]!.data.status).toBe('starting');
      expect(hc[1]!.data).toMatchObject({ previous: 'starting', status: 'healthy' });
      expect(hc[2]!.data.healthy).toBe(true);
      expect(hc.every((g) => g.data.container === 'probe-app-1')).toBe(true);
      expect(cap.got.find((g) => g.event === 'container.ready')!.data).toMatchObject({
        container: 'probe-app-1',
        hasHealthcheck: true,
      });
    } finally {
      await flip;
      cap.off();
      await s.stop();
      docker.restore();
    }
  }, 20_000);

  test('a crash on boot fails the stack; a one-shot that exits 0 does not', async () => {
    // Both halves in one test on purpose: "exited" alone must NOT mean failure, or every stack with
    // a migration container would be permanently broken. The pair is what proves the rule is exit
    // CODE, not exit.
    const crash = mutableDocker();
    crash.set({ state: 'exited', exit: 1 });
    const a = await boot(crash);
    const cap = await capture(a);
    try {
      await deploy(a);
      const snap = await read(a);
      expect(snap.state).toBe('failed');
      expect(snap.containers[0]!.failed).toBe(true);
      expect(snap.containers[0]!.reason).toBe('exited with code 1');
      await cap.until((n) => n === 'stack.failed');
      expect(cap.got.find((g) => g.event === 'container.start-failed')!.data).toMatchObject({
        container: 'probe-app-1',
        exitCode: 1,
      });
      expect(cap.got.find((g) => g.event === 'stack.failed')!.data).toMatchObject({
        failedContainers: ['probe-app-1'],
      });
    } finally {
      cap.off();
      await a.stop();
      crash.restore();
    }

    const done = mutableDocker();
    done.set({ state: 'exited', exit: 0 });
    const b = await boot(done);
    try {
      await deploy(b);
      expect((await read(b)).state).toBe('ready');
    } finally {
      await b.stop();
      done.restore();
    }
  }, 20_000);

  test('a crash LOOP is reported, and a single restart is forgiven', async () => {
    // The commonest crash shape in a preview stack, and the one a state sample cannot see: with a
    // `restart:` policy the container cycles exited → restarting → running, so a poll lands on a
    // healthy-looking sample often enough that `state` alone is a coin flip. RestartCount only goes
    // up, so it decides. The second half is the guard against over-eagerness: an app that dies once
    // waiting for its database is the normal case, not a failure.
    const loop = mutableDocker();
    loop.set({ state: 'restarting', exit: 1, restarts: 5 });
    // The DEFAULT deadline on purpose: this must settle because the crash loop was detected, not
    // because a short clock ran out — a 400ms deadline made it settle `timedout` under a loaded CI
    // box and the assertion below would then be measuring the wrong thing.
    const a = await boot(loop);
    const cap = await capture(a);
    try {
      await deploy(a);
      const snap = await read(a);
      expect(snap.state).toBe('failed');
      expect(snap.containers[0]!.restartCount).toBe(5);
      expect(snap.containers[0]!.reason).toContain('crash loop');
      await cap.until((n) => n === 'container.start-failed');
      expect(cap.names()).toContain('container.start-failed');
    } finally {
      cap.off();
      await a.stop();
      loop.restore();
    }

    const blip = mutableDocker();
    blip.set({ state: 'running', restarts: 1 });
    const b = await boot(blip);
    try {
      await deploy(b);
      expect((await read(b)).state).toBe('ready');
    } finally {
      await b.stop();
      blip.restore();
    }
  }, 20_000);

  test('a healthcheck that never settles times out, and names what it was still waiting on', async () => {
    const docker = mutableDocker();
    docker.set({ health: 'starting' }); // and never changes
    const s = await boot(docker, { timeoutMs: 150 });
    const cap = await capture(s);
    try {
      await deploy(s);
      const snap = await read(s);
      expect(snap.state).toBe('timedout');
      await cap.until((n) => n === 'stack.timedout');
      expect(cap.got.find((g) => g.event === 'healthcheck.timedout')!.data).toMatchObject({
        container: 'probe-app-1',
        status: 'starting',
      });
      // The list is the difference between a support thread and a container to go and look at.
      expect(cap.got.find((g) => g.event === 'stack.timedout')!.data).toMatchObject({
        pendingContainers: ['probe-app-1'],
      });
    } finally {
      cap.off();
      await s.stop();
      docker.restore();
    }
  }, 20_000);

  test('merely READING readiness never emits — a page view must not invent a failure', async () => {
    // The bug this guards: GET on a deployment nobody deployed finds no containers, converges on
    // nothing, and at the deadline would announce "did not become ready in time" to every notifier.
    // A read may start a watch; it may not narrate one.
    const docker = mutableDocker();
    docker.empty();
    const s = await boot(docker, { timeoutMs: 120 });
    const cap = await capture(s);
    try {
      await putSpec(s); // submitted, never deployed
      const snap = await read(s);
      expect(snap.state).toBe('timedout'); // the CALLER is told, in full
      expect(snap.containers).toEqual([]);
      // The positive control: the PUT's own event reaches the tap, so a silent bus below is silence
      // about readiness and not a tap that never worked. Then give a wrongly-emitted timeout the
      // round trip it would need to arrive.
      await cap.until((n) => n === 'deployment.created');
      await Bun.sleep(300);
      // …and the bus hears nothing about readiness at all.
      expect(cap.names().filter((n) => /^(stack|container|healthcheck)\./.test(n))).toEqual([]);
    } finally {
      cap.off();
      await s.stop();
      docker.restore();
    }
  }, 20_000);

  test('every watch ends in exactly one terminal event — a subscriber never waits forever', async () => {
    // The property the whole design rests on. Asserted across all three outcomes, because a stream
    // that only fires on success is the failure mode this is guarding against.
    // Only the timeout case gets a short deadline. Giving one to the ready/failed cases would let a
    // slow machine settle them by running out the clock — the test would still be green and would
    // have stopped testing convergence.
    for (const [setup, expected, timeoutMs] of [
      [{}, 'stack.ready', 5_000],
      [{ state: 'exited', exit: 3 }, 'stack.failed', 5_000],
      [{ health: 'starting' }, 'stack.timedout', 150],
    ] as const) {
      const docker = mutableDocker();
      docker.set(setup);
      const s = await boot(docker, { timeoutMs });
      const cap = await capture(s);
      try {
        await deploy(s);
        await read(s);
        await cap.until((n) => n.startsWith('stack.'));
        await Bun.sleep(200); // a second terminal event would need one more round trip to show
        const terminal = cap.names().filter((n) => n.startsWith('stack.'));
        expect(terminal).toEqual([expected]);
      } finally {
        cap.off();
        await s.stop();
        docker.restore();
      }
    }
  }, 30_000);
});
