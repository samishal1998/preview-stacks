/**
 * `down` with `{ forget: true }`, and the deployment id every job now carries.
 *
 * Tear down and forget is one job: it tears the stack down as usual and, only if the teardown
 * finished clean — no failed step, no leak, docker confirming no container is left — removes the
 * deployment's record too. Anything less keeps the record, so whatever was left behind stays visible
 * and the teardown can be retried. These tests drive it through the real binary against a stand-in
 * `docker` that reports no containers, so the axes decide clean or leaked.
 */
import { describe, expect, test } from 'bun:test';
import { type Booted, bootServer, until } from '../harness/server.ts';
import { dockerShim } from '../harness/docker-shim.ts';

type Job = {
  id: string;
  stack: string;
  deployment: string | null;
  action: string;
  state: string;
  log: { level: string; message: string }[];
};

const spec = (stack: string, hooks: Record<string, string>) =>
  `version: 1\nstack: ${stack}\naxes:\n  - name: a\n` +
  Object.entries(hooks)
    .map(([k, v]) => `    ${k}: ${JSON.stringify(v)}\n`)
    .join('');

const put = (s: Booted, id: string, body: string) =>
  fetch(`${s.base}/api/deployments/${id}`, { method: 'PUT', headers: s.H, body: JSON.stringify({ spec: body }) });

const post = (s: Booted, id: string, action: string, body?: unknown) =>
  fetch(`${s.base}/api/deployments/${id}/${action}`, {
    method: 'POST',
    headers: s.H,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });

const job = async (s: Booted, id: string) =>
  ((await (await fetch(`${s.base}/api/jobs/${id}`, { headers: s.H })).json()) as { job: Job }).job;

const settle = async (s: Booted, id: string) => {
  const j = await until(() => job(s, id), (v) => !!v && v.state !== 'queued' && v.state !== 'running', 20_000);
  if (j.state === 'queued' || j.state === 'running') throw new Error(`job ${id} never finished`);
  return j;
};

const said = (j: Job, fragment: string) => j.log.some((e) => e.message.includes(fragment));

async function withServer(tag: string, run: (s: Booted) => Promise<void>) {
  const docker = dockerShim('');
  const s = await bootServer({ tag, pathPrefix: docker.dir, readiness: { pollMs: 20, timeoutMs: 200 } });
  try {
    await run(s);
  } finally {
    await s.stop();
    docker.remove();
  }
}

describe('API: tear down and forget', () => {
  // negative control: drop the `if o.Forget` call in startLifecycle — the job succeeds, the record
  // survives, and the GET below answers 200 instead of 404.
  test('a clean teardown forgets the deployment, in the same job', async () => {
    await withServer('forget-clean', async (s) => {
      expect((await put(s, 'pr-1', spec('pr-one', { up: 'true', down: 'true', assert_gone: 'true' }))).status).toBe(201);
      const r = await post(s, 'pr-1', 'down', { forget: true });
      expect(r.status).toBe(202);
      const stub = ((await r.json()) as { job: Job }).job;
      expect(stub.deployment).toBe('pr-1');

      const done = await settle(s, stub.id);
      expect(done.state).toBe('ok');
      expect(said(done, 'forgotten')).toBe(true);
      expect((await fetch(`${s.base}/api/deployments/pr-1`, { headers: s.H })).status).toBe(404);
    });
  }, 40_000);

  // negative control: forget whenever the job ends, leak or not — the leaked deployment's record is
  // removed and the GET below answers 404.
  test('a teardown that leaks keeps the record, and says why', async () => {
    await withServer('forget-leak', async (s) => {
      await put(s, 'pr-2', spec('pr-two', { up: 'true', down: 'true', assert_gone: 'false' }));
      const stub = ((await (await post(s, 'pr-2', 'down', { forget: true })).json()) as { job: Job }).job;
      const done = await settle(s, stub.id);
      expect(done.state).not.toBe('ok');
      expect(said(done, 'kept the record')).toBe(true);
      expect((await fetch(`${s.base}/api/deployments/pr-2`, { headers: s.H })).status).toBe(200);
    });
  }, 40_000);

  // negative control: drop the verify check in the lifecycle route — forget with verify off is
  // accepted (202) and a teardown nobody checked removes the one record that could show a leak.
  test('forget is refused without the leftovers check, and on anything but down', async () => {
    await withServer('forget-refused', async (s) => {
      await put(s, 'pr-3', spec('pr-three', { up: 'true', down: 'true', assert_gone: 'true' }));
      const noVerify = await post(s, 'pr-3', 'down', { forget: true, verify: false });
      expect(noVerify.status).toBe(400);
      expect(((await noVerify.json()) as { error: string }).error).toContain('needs `verify`');
      const onUp = await post(s, 'pr-3', 'up', { forget: true });
      expect(onUp.status).toBe(400);
      expect((await fetch(`${s.base}/api/deployments/pr-3`, { headers: s.H })).status).toBe(200);
    });
  }, 40_000);

  // negative control: build the job record without its deployment — the jobs list reads null and the
  // UI cannot link a job to what it acted on.
  test('every job names the deployment it acted on', async () => {
    await withServer('job-deployment', async (s) => {
      await put(s, 'pr-4', spec('pr-four', { up: 'true', down: 'true', assert_gone: 'true' }));
      const stub = ((await (await post(s, 'pr-4', 'verify')).json()) as { job: Job }).job;
      await settle(s, stub.id);
      const list = ((await (await fetch(`${s.base}/api/jobs`, { headers: s.H })).json()) as { jobs: Job[] }).jobs;
      const mine = list.find((j) => j.id === stub.id)!;
      expect(mine.stack).toBe('pr-four');
      expect(mine.deployment).toBe('pr-4');
    });
  }, 40_000);
});
