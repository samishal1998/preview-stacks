/**
 * Stopping a job, following logs, and acting on one container at a time — ported black-box from
 * packages/pstack/test/stack.test.ts ('stopping a job, and following logs', 'one container at a
 * time: start, stop, restart'). Events are observed through the event tap (a `*` webhook notifier),
 * docker through a recording shim on the child's PATH.
 */
import { describe, expect, test } from 'bun:test';
import { existsSync, rmSync } from 'node:fs';
import { DEFAULT_TOKEN, bootServer, tmpd, until, type Booted } from '../harness/server.ts';
import { arm, dockerShim, sq } from '../harness/docker-shim.ts';
import { eventTap } from '../harness/receiver.ts';

describe('stopping a job, and following logs', () => {
  const put = (s: Booted, spec: string) =>
    fetch(`${s.base}/api/deployments/d`, {
      method: 'PUT',
      headers: s.H,
      body: JSON.stringify({ spec }),
    });

  const jobOf = async (s: Booted, jobId: string) =>
    (await (await fetch(`${s.base}/api/jobs/${jobId}`, { headers: s.H })).json()) as {
      job: { state: string; startedAt: number; endedAt?: number; cancelledBy?: string };
    };

  test('cancel kills the running command — the shell stops, not just the record', async () => {
    /*
     * The point of the whole feature: before this, the only way to stop a wedged 40-minute build was
     * to restart the control plane and lose every other job's transcript. A cancel that merely
     * flipped a field would be worse than none — the page would say "cancelled" while the host went
     * on building. So the assertion is on the CLOCK: `sleep 45` has to end in well under 45 seconds.
     */
    const s = await bootServer({ tag: 'cancel' });
    const tap = await eventTap(s.base, s.H);
    try {
      await put(s, 'version: 1\nstack: slow\naxes:\n  - name: a\n    up: "sleep 45"\n');
      const started = await fetch(`${s.base}/api/deployments/d/up`, { method: 'POST', headers: s.H });
      const { job } = (await started.json()) as { job: { id: string } };

      // Wait until the hook is actually running, or the cancel would race the spawn.
      for (let i = 0; i < 100 && (await jobOf(s, job.id)).job.state !== 'running'; i++) {
        await Bun.sleep(10);
      }
      const at = Date.now();
      const r = await fetch(`${s.base}/api/jobs/${job.id}/cancel`, { method: 'POST', headers: s.H });
      expect(r.status).toBe(200);
      const body = (await r.json()) as { by: string; warning: string };
      expect(body.by).toBe('root (PSTACK_TOKEN)');
      // The answer says what stopping does NOT do, in the same breath as reporting success.
      expect(body.warning).toContain('Nothing was undone');

      let final = await jobOf(s, job.id);
      for (let i = 0; i < 300 && final.job.state === 'running'; i++) {
        await Bun.sleep(10);
        final = await jobOf(s, job.id);
      }
      expect(final.job.state).toBe('cancelled'); // NOT 'failed' — it was stopped, it did not lose
      expect(final.job.cancelledBy).toBe('root (PSTACK_TOKEN)');
      expect(Date.now() - at).toBeLessThan(10_000); // `sleep 45` really died
      // The tap hears the event a beat after the job record flips — wait for the delivery, not a guess.
      const got = await until(async () => tap.events(), (e) => e.includes('job.cancelled'));
      expect(got).toContain('job.cancelled');
      expect(got).not.toContain('job.failed');

      // Cancelling a finished job is 409 (the request is out of date), not 404 (the id is wrong).
      const again = await fetch(`${s.base}/api/jobs/${job.id}/cancel`, { method: 'POST', headers: s.H });
      expect(again.status).toBe(409);
      const missing = await fetch(`${s.base}/api/jobs/nope/cancel`, { method: 'POST', headers: s.H });
      expect(missing.status).toBe(404);
    } finally {
      tap.stop();
      await s.stop();
    }
  }, 30_000);

  test('a cancelled teardown stops at the current axis instead of running the rest', async () => {
    // Teardown is best-effort and keeps going PAST failures by design (stack.ts), so a killed
    // command alone would not stop it — the runner has to refuse every later one too. Without that,
    // pressing stop and watching the remaining hooks run is exactly what happens.
    const s = await bootServer({ tag: 'cancel' });
    const marker = `${tmpd('cancel')}-ran`;
    try {
      await put(
        s,
        // `down` runs axes in REVERSE declaration order, so the sleeper is declared LAST to be the
        // one running when the cancel lands, and `earlier` is what must never get its turn.
        'version: 1\nstack: slowdown\naxes:\n' +
          `  - name: earlier\n    down: "touch ${marker}"\n` +
          '  - name: sleeper\n    down: "sleep 30"\n',
      );
      const started = await fetch(`${s.base}/api/deployments/d/down`, {
        method: 'POST',
        headers: s.H,
        body: JSON.stringify({ verify: false }),
      });
      const { job } = (await started.json()) as { job: { id: string } };
      for (let i = 0; i < 100 && (await jobOf(s, job.id)).job.state !== 'running'; i++) {
        await Bun.sleep(10);
      }
      await Bun.sleep(150); // let it reach the first axis
      await fetch(`${s.base}/api/jobs/${job.id}/cancel`, { method: 'POST', headers: s.H });

      let final = await jobOf(s, job.id);
      for (let i = 0; i < 300 && final.job.state === 'running'; i++) {
        await Bun.sleep(10);
        final = await jobOf(s, job.id);
      }
      expect(final.job.state).toBe('cancelled');
      expect(existsSync(marker)).toBe(false);
    } finally {
      rmSync(marker, { force: true });
      await s.stop();
    }
  }, 30_000);

  test('following logs streams redacted lines and ends by itself', async () => {
    // A fake `docker` that prints one line carrying this server's own token. Two things at once:
    // the SSE framing is real (a line in, a frame out), and the redaction the fetched read promises
    // applies to the followed one too — a live log is exactly as likely to print a credential.
    //
    // Long on purpose: redaction ignores short values (a 3-character "secret" would mask every
    // ordinary word containing it), so a toy token would make the redaction assertion below vacuous.
    const TOKEN = DEFAULT_TOKEN;
    const shim = dockerShim(arm('*', `app-1  | connecting with ${TOKEN}`));
    const s = await bootServer({ tag: 'tail', pathPrefix: shim.dir });
    try {
      await put(
        s,
        'version: 1\nstack: tailed\ncompose:\n  file: docker-compose.yml\n  profiles: [web]\n' +
          'axes:\n  - name: a\n    up: "true"\n',
      );
      const res = await fetch(`${s.base}/api/deployments/d/logs/stream?tail=10`, { headers: s.H });
      expect(res.status).toBe(200);
      expect(res.headers.get('content-type')).toBe('text/event-stream');

      const text = await res.text(); // compose exits at once, so the stream closes on its own
      expect(text).toContain('connecting with');
      expect(text).not.toContain(TOKEN); // …masked, never the token itself
      expect(text).toContain('"done":true');
    } finally {
      await s.stop();
      shim.remove();
    }
  }, 20_000);
});

describe('one container at a time: start, stop, restart', () => {
  /**
   * An arm matching `"$*"` by PREFIX. The harness's `arm()` passes a pattern containing `*` through
   * unquoted, and sh cannot parse an unquoted case pattern with a space in it (`ps -aq *`), so the
   * prefix is quoted here and only the glob is left bare.
   */
  const prefixArm = (prefix: string, text: string) => `  ${JSON.stringify(prefix)}*) printf '%s\\n' ${sq(text)}; exit 0 ;;`;

  /**
   * A fake `docker` that reports one container for this deployment and RECORDS every argv it is
   * handed. The recording is the point: the guard below is only meaningful if the allowed path
   * really shells out, and the refusals are only meaningful if they don't.
   */
  const docker = () =>
    dockerShim(
      [
        prefixArm('ps -aq ', 'c0ffee123456'),
        prefixArm(
          'inspect ',
          '[{"Id":"c0ffee123456","Name":"/probe-app-1","RestartCount":0,"Config":{"Image":"nginx","Labels":{"com.docker.compose.service":"app","com.docker.compose.project":"probe"}},"State":{"Status":"running","ExitCode":0},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"172.20.0.5"}},"Ports":{}}}]',
        ),
      ].join('\n'),
      { record: true },
    );

  const boot = (shim: { dir: string }) =>
    bootServer({
      tag: 'cact',
      pathPrefix: shim.dir,
      // Keep the readiness watch a start/restart kicks off from outliving the test.
      readiness: { pollMs: 20, timeoutMs: 200 },
    });

  const putSpec = (s: Booted) =>
    fetch(`${s.base}/api/deployments/d`, {
      method: 'PUT',
      headers: s.H,
      body: JSON.stringify({ spec: 'version: 1\nstack: probe\naxes:\n  - name: a\n    up: "true"\n' }),
    });

  const post = (s: Booted, name: string, action: string, headers = s.H) =>
    fetch(`${s.base}/api/deployments/d/containers/${encodeURIComponent(name)}/${action}`, { method: 'POST', headers });

  test('a container this deployment does not own is refused — never handed to docker', async () => {
    // The same host escape the terminal guards, with more blast radius: `docker stop traefik` takes
    // down every preview on the box at once, and `pstack-control` is the thing being asked.
    const shim = docker();
    const s = await boot(shim);
    try {
      await putSpec(s);
      for (const name of ['pstack-control', 'traefik', 'c0ffee999999', '../../etc/passwd']) {
        const r = await post(s, name, 'restart');
        expect(`${name} → ${r.status}`).toBe(`${name} → 404`);
      }
      // Nothing reached docker's own verbs — only the ownership lookup (ps/inspect) did.
      expect(shim.calls().filter((c) => /^(stop|start|restart)/.test(c))).toEqual([]);

      // …and the container it DOES own goes through, so the refusals refuse something real.
      const ok = await post(s, 'probe-app-1', 'restart');
      expect(ok.status).toBe(200);
      expect(shim.calls()).toContain('restart -t 10 probe-app-1');
    } finally {
      await s.stop();
      shim.remove();
    }
  }, 20_000);

  test('each verb runs its own docker command, and start takes no grace period', async () => {
    const shim = docker();
    const s = await boot(shim);
    try {
      await putSpec(s);
      expect((await post(s, 'probe-app-1', 'stop')).status).toBe(200);
      expect((await post(s, 'probe-app-1', 'start')).status).toBe(200);
      const calls = shim.calls();
      // `-t` is how long docker waits before SIGKILL — meaningless on start, so it is not sent.
      expect(calls).toContain('stop -t 10 probe-app-1');
      expect(calls).toContain('start probe-app-1');
      expect(calls.some((c) => c.startsWith('start -t'))).toBe(false);

      // Clamped, not passed through: an unbounded grace is a request that never returns.
      await fetch(`${s.base}/api/deployments/d/containers/probe-app-1/stop?grace=9999`, {
        method: 'POST',
        headers: s.H,
      });
      expect(shim.calls()).toContain('stop -t 120 probe-app-1');
    } finally {
      await s.stop();
      shim.remove();
    }
  }, 20_000);

  test('stopping cancels the readiness watch; restarting starts one', async () => {
    // A watch left running through a deliberate stop would announce `stack.failed` about a container
    // someone meant to stop — a false alarm sent to every notifier.
    const shim = docker();
    const s = await boot(shim);
    const tap = await eventTap(s.base, s.H);
    try {
      await putSpec(s);
      await post(s, 'probe-app-1', 'stop');
      let got = await until(async () => tap.events(), (e) => e.includes('container.stopped'));
      await Bun.sleep(300); // past the watch deadline, had one been left running
      got = tap.events();
      expect(got).toContain('container.stopped');
      expect(got.filter((n) => n.startsWith('stack.'))).toEqual([]);

      tap.got.length = 0;
      await post(s, 'probe-app-1', 'restart');
      // The container the shim reports is running, so the watch settles ready — the answer to
      // "did it come back" arrives without anyone asking for it.
      got = await until(async () => tap.events(), (e) => e.includes('stack.ready'));
      expect(got).toContain('container.restarted');
      expect(got).toContain('stack.ready');
    } finally {
      tap.stop();
      await s.stop();
      shim.remove();
    }
  }, 20_000);

  test('unauthenticated, an unknown verb, and a GET are all refused', async () => {
    const shim = docker();
    const s = await boot(shim);
    try {
      await putSpec(s);
      expect((await post(s, 'probe-app-1', 'restart', {})).status).toBe(401);
      // An unknown verb does not match the route at all — 404, and nothing is invented from it.
      expect((await post(s, 'probe-app-1', 'obliterate')).status).toBe(404);
      const g = await fetch(`${s.base}/api/deployments/d/containers/probe-app-1/stop`, { headers: s.H });
      expect(g.status).toBe(405);
      expect(shim.calls().filter((c) => /^(stop|start|restart)/.test(c))).toEqual([]);
    } finally {
      await s.stop();
      shim.remove();
    }
  }, 20_000);
});

describe('the per-stack queue, the global cap, and cancelling a stack', () => {
  /**
   * Depth-one queueing replaced the flat refusal a busy stack used to get. Before it, five pushes
   * to a PR in one minute produced one deploy and four `409`s, and CI reported red for a stack
   * that was merely busy. What did NOT change is the guarantee the product is built on — one job
   * per stack at a time — so nothing here ever asserts two jobs running on one stack; it asserts
   * that the extra ones WAIT.
   *
   * These are clock-free where it matters. `Start` dispatches inline when a slot is free, so the
   * `202` stub already says `running` or `queued` at the instant of acceptance: the stubs are the
   * proof, and `startedAt`/`endedAt` only confirm the ordering afterwards. The one place a clock
   * IS the assertion is preemption and cancellation, where a `sleep 20` that really died is the
   * difference between stopping a job and flipping a field.
   */
  type Rec = {
    id: string;
    state: string;
    startedAt: number | null;
    endedAt?: number;
    cancelledBy?: string;
    log: { level: string; message: string }[];
  };
  type Stub = { id: string; stack: string; action: string; state: string };

  const spec = (stack: string, hooks: Record<string, string>) =>
    `version: 1\nstack: ${stack}\naxes:\n  - name: a\n` +
    Object.entries(hooks)
      .map(([k, v]) => `    ${k}: ${JSON.stringify(v)}\n`)
      .join('');

  const boot = (tag: string, env?: Record<string, string>) =>
    // A short readiness watch: an `up` hands off to one, and a 180s default would outlive the test.
    bootServer({ tag, env, readiness: { pollMs: 20, timeoutMs: 200 } });

  const put = (s: Booted, id: string, body: string) =>
    fetch(`${s.base}/api/deployments/${id}`, { method: 'PUT', headers: s.H, body: JSON.stringify({ spec: body }) });

  /** POST a lifecycle action and return the 202's stub — the accepted-at shape, before any polling. */
  const start = async (s: Booted, id: string, action: string, body?: unknown): Promise<Stub> => {
    const r = await fetch(`${s.base}/api/deployments/${id}/${action}`, {
      method: 'POST',
      headers: s.H,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    expect(`${id}/${action} → ${r.status}`).toBe(`${id}/${action} → 202`);
    return ((await r.json()) as { job: Stub }).job;
  };

  const rec = async (s: Booted, id: string) =>
    ((await (await fetch(`${s.base}/api/jobs/${id}`, { headers: s.H })).json()) as { job: Rec }).job;

  const over = (state: string) => state !== 'queued' && state !== 'running';

  /** The harness's `waitJob` returns the moment a job is not `running` — which a QUEUED job never is. */
  const settle = async (s: Booted, id: string, ms = 30_000) => {
    const j = await until(() => rec(s, id), (v) => !!v && over(v.state), ms);
    if (!j || !over(j.state)) throw new Error(`job ${id} never reached a terminal state (state ${j?.state})`);
    return j;
  };

  /** `busy` is running || queued || held, and it clears one beat AFTER the job record goes terminal. */
  const idle = async (s: Booted, id: string) => {
    const d = await until(
      async () => (await (await fetch(`${s.base}/api/deployments/${id}`, { headers: s.H })).json()) as { busy: boolean | null },
      (v) => v.busy === false,
      10_000,
    );
    expect(d.busy).toBe(false);
  };

  const said = (j: Rec, fragment: string) => j.log.some((e) => e.message.includes(fragment));

  // negative control: reinstate the old refusal — `if r.held[stackName] {` in jobs.go's Start
  // becomes `if r.held[stackName] || r.live[stackName] != nil {`. The second POST is a 409 again.
  test('a second lifecycle POST for a busy stack is queued, not refused — and runs when the first ends', async () => {
    const s = await boot('queue');
    try {
      await put(s, 'd', spec('qdepth', { up: 'sleep 2' }));
      const first = await start(s, 'd', 'up');
      const second = await start(s, 'd', 'up');
      // Both 202. The stub states are the whole claim, read at acceptance time: no poll, no clock.
      expect(`${first.state} then ${second.state}`).toBe('running then queued');

      // The record exists under the id the caller was handed, from the moment of acceptance — a
      // client that starts polling immediately must find it, not a 404 that resolves later.
      const waiting = await rec(s, second.id);
      expect(waiting.state).toBe('queued');
      expect(waiting.startedAt).toBe(null); // invariant 11: null, never 0, and never absent

      const a = await settle(s, first.id);
      const b = await settle(s, second.id);
      expect(`${a.state} then ${b.state}`).toBe('ok then ok');
      expect(typeof b.startedAt).toBe('number');
      // The product guarantee, in one comparison: the second did not begin until the first was over.
      expect(b.startedAt! >= a.endedAt!).toBe(true);
    } finally {
      await s.stop();
    }
  }, 60_000);

  // negative control: in jobs.go's Start, disable the supersede branch
  // (`if old := r.queue[stackName]; old != nil {` → `; false {`) so the newer job merely overwrites
  // `r.queue[stack]`. The replaced record is exactly the silent vanishing this state exists to
  // prevent: it stays `queued` forever and its stream never closes.
  test('a third POST replaces the queued one: it ends `superseded` under its own id, its stream closes, and only two jobs run', async () => {
    const s = await boot('supersede');
    // Absolute: a submitted spec's hooks run with cwd set to the deployment's own directory.
    const marker = `${tmpd('supersede')}-ran`;
    try {
      await put(s, 'd', spec('qsuper', { up: `sleep 2; printf 'x\\n' >> ${marker}` }));
      const first = await start(s, 'd', 'up');
      const replaced = await start(s, 'd', 'up');
      // Opened while it is still queued: the stream a client is watching must not be left hanging
      // when the job behind it is dropped for a newer one — that is a page that spins forever.
      const stream = await fetch(`${s.base}/api/jobs/${replaced.id}/stream`, { headers: s.H });
      expect(stream.status).toBe(200);
      const closed = Promise.race([stream.text(), Bun.sleep(10_000).then(() => 'STREAM NEVER CLOSED')]);

      const third = await start(s, 'd', 'up');
      expect(`${first.state}/${replaced.state}/${third.state}`).toBe('running/queued/queued');

      const gone = await settle(s, replaced.id);
      // NOT `cancelled`: nobody stopped it and nothing ran, so there is no partial state to hunt.
      expect(gone.state).toBe('superseded');
      expect(gone.startedAt).toBe(null);
      expect(typeof gone.endedAt).toBe('number'); // terminal, so a client polling this id stops
      expect(said(gone, `superseded by ${third.id}`)).toBe(true);
      expect(await closed).toContain('"done":true');

      expect((await settle(s, first.id)).state).toBe('ok');
      expect((await settle(s, third.id)).state).toBe('ok');
      // Depth one, corroborated by the host itself: two hooks ran, not three.
      expect((await Bun.file(marker).text()).trimEnd().split('\n')).toEqual(['x', 'x']);
    } finally {
      rmSync(marker, { force: true });
      await s.stop();
    }
  }, 60_000);

  // negative control: empty jobs.go's `preempts` map (drop the `Down: true` row). The teardown
  // queues behind the `sleep 20` deploy and the `up` is never cancelled.
  test('`down` preempts: the running `up` is cancelled, the job behind it is dropped, and the teardown runs now', async () => {
    // A teardown queued behind a deploy is the shape of an incident: the operator asked for the
    // stack to be gone and watches a build they no longer want finish first.
    const s = await boot('preempt');
    try {
      await put(s, 'd', spec('qpreempt', { up: 'sleep 20', down: 'true' }));
      const up = await start(s, 'd', 'up');
      const behind = await start(s, 'd', 'verify');
      expect(`${up.state}/${behind.state}`).toBe('running/queued');

      const at = Date.now();
      const down = await start(s, 'd', 'down', { verify: false });
      // `queued`, and that is correct: the teardown takes the stack the instant the cancelled
      // `up`'s shell returns and not one moment before. Two jobs on one stack never happens.
      expect(down.state).toBe('queued');

      const killed = await settle(s, up.id);
      expect(killed.state).toBe('cancelled');
      expect(killed.cancelledBy).toBe('a down for this stack');
      // The line that stops an operator believing a preempted deploy rolled itself back. It reaches
      // the transcript on THIS path too, where no person typed cancel.
      expect(said(killed, 'whatever ran before this point was NOT undone')).toBe(true);

      const dropped = await settle(s, behind.id);
      // Also `cancelled`, but a different fact, and the transcript is where the difference lives:
      // this one never ran, so sending its operator to look for partial state would be a lie.
      expect(dropped.state).toBe('cancelled');
      expect(dropped.startedAt).toBe(null);
      expect(said(dropped, 'before it started — nothing ran, so there is nothing to undo')).toBe(true);
      expect(said(dropped, 'NOT undone')).toBe(false);

      expect((await settle(s, down.id)).state).toBe('ok');
      // `sleep 20` was the hook that was running. Waiting for it is what queueing would look like.
      expect(Date.now() - at).toBeLessThan(15_000);
    } finally {
      await s.stop();
    }
  }, 60_000);

  // negative control: in jobs.go's pump, turn the cap guard `if r.inFlight >= r.maxRunning {` into
  // `if false {`. All four dispatch inline and every stub says `running`.
  test('PSTACK_MAX_JOBS caps how many run at once across stacks — over it they wait, in acceptance order', async () => {
    const s = await boot('cap', { PSTACK_MAX_JOBS: '2' });
    try {
      const ids = ['c1', 'c2', 'c3', 'c4'];
      for (const id of ids) await put(s, id, spec(`cap-${id}`, { up: 'sleep 2' }));
      const stubs: Stub[] = [];
      for (const id of ids) stubs.push(await start(s, id, 'up'));
      // FOUR DIFFERENT STACKS, so the per-stack lock cannot explain this — only the global cap can.
      // And they wait rather than fail: every one of the four was a 202.
      expect(stubs.map((j) => j.state)).toEqual(['running', 'running', 'queued', 'queued']);

      const recs: Rec[] = [];
      for (const j of stubs) recs.push(await settle(s, j.id, 40_000));
      expect(recs.map((j) => j.state)).toEqual(['ok', 'ok', 'ok', 'ok']);

      // Concurrency MEASURED, not sampled: sweep the [startedAt, endedAt] intervals and take the
      // maximum overlap. `<= 2` would pass with a cap of one, and would pass for a sweep that
      // happened to observe nothing at all; the cap is two, so exactly two must have overlapped.
      const edges = recs.flatMap((j) => [
        { t: j.endedAt!, d: -1 },
        { t: j.startedAt!, d: +1 },
      ]);
      // A job ending at t frees the slot the job starting at t takes: -1 sorts before +1.
      edges.sort((a, b) => a.t - b.t || a.d - b.d);
      let live = 0;
      let peak = 0;
      for (const e of edges) peak = Math.max(peak, (live += e.d));
      expect(peak).toBe(2);

      // FIFO across stacks: the third accepted took the first slot to free, and neither waiter
      // started before a slot existed. One stack cannot starve the others.
      const [first, second, third, fourth] = recs as [Rec, Rec, Rec, Rec];
      expect(third.startedAt! <= fourth.startedAt!).toBe(true);
      expect(Math.min(third.startedAt!, fourth.startedAt!) >= Math.min(first.endedAt!, second.endedAt!)).toBe(true);
    } finally {
      await s.stop();
    }
  }, 90_000);

  // negative control: in jobs.go's clearStackLocked, disable the queued branch
  // (`if e := r.queue[stack]; e != nil {` → `; false {`). The cancel reports only the running job
  // and the waiter dispatches the moment the running one dies.
  test('cancelling a stack empties both — the running job and the one waiting behind it', async () => {
    const s = await boot('cancelstack');
    try {
      await put(s, 'd', spec('qclear', { up: 'sleep 20' }));
      const running = await start(s, 'd', 'up');
      const waiting = await start(s, 'd', 'up');
      expect(`${running.state}/${waiting.state}`).toBe('running/queued');

      const at = Date.now();
      const r = await fetch(`${s.base}/api/deployments/d/cancel`, { method: 'POST', headers: s.H });
      expect(r.status).toBe(200);
      const body = (await r.json()) as { stack: string; cancelled: Stub[]; by: string; warning: string };
      expect(body.stack).toBe('qclear');
      expect(body.by).toBe('root (PSTACK_TOKEN)');
      // BOTH, named, in one call — the answer says what it acted on rather than a bare 200, and the
      // running one comes first because that is the one that leaves something behind. Their states
      // differ in the same breath and honestly: the queued job is terminal already (nothing to
      // wind down), the running one is still `running` until its shell actually returns.
      expect(body.cancelled.map((j) => `${j.id}:${j.state}`)).toEqual([`${running.id}:running`, `${waiting.id}:cancelled`]);
      expect(body.warning).toContain('Nothing was undone');

      const a = await settle(s, running.id);
      const b = await settle(s, waiting.id);
      expect(`${a.state}/${b.state}`).toBe('cancelled/cancelled');
      expect(b.startedAt).toBe(null); // it never ran — the same distinction preemption draws
      expect(Date.now() - at).toBeLessThan(15_000); // `sleep 20` really died, the record did not just flip

      // Empty means empty: the stack takes the next job at once, and it DISPATCHES rather than queues.
      await idle(s, 'd');
      expect((await start(s, 'd', 'verify')).state).toBe('running');
    } finally {
      await s.stop();
    }
  }, 60_000);
});
