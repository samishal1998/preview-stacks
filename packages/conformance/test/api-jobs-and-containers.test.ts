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
