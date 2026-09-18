/**
 * Loki's settings over HTTP: the routes, the apply job, its rollback, and the boot reconcile.
 *
 * The docker shim plays the control stack's `loki` container (`l1`) and the four commands an apply
 * issues: verify, restart, the health probe, logs. PSTACK_LOKI_DIR is a temp copy of the rendered
 * loki/config.yaml golden. PSTACK_LOKI_UID is this process's uid, so a non-root runner reaches the
 * S3 path. A Bun server on 127.0.0.1 plays S3 for the probe and does not check signatures.
 */
import { describe, expect, test } from 'bun:test';
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { bootServer, tmpd, until, type Booted } from '../harness/server.ts';
import { arm, casePattern, dockerShim, sq, type Shim } from '../harness/docker-shim.ts';
import { GOLDEN } from '../harness/goldens.ts';

/** Loki's config as `pstack init --logging loki` writes it: the default render. */
const DEFAULT = readFileSync(join(GOLDEN, 'render', 'control', 'http01-basic-compose-loki', 'loki', 'config.yaml'), 'utf8');

const PS = 'ps -aq --filter label=com.docker.compose.project=pstack-control';
const INSPECT = JSON.stringify([
  {
    Id: 'l1',
    Name: '/pstack-control-loki-1',
    Config: {
      Image: 'grafana/loki:3.7.7',
      Labels: {
        'com.docker.compose.service': 'loki',
        'pstack.logging.push-url': 'https://pstack:x@loki.example.com/loki/api/v1/push',
      },
    },
    State: { Status: 'running', StartedAt: '2026-09-15T08:00:00Z' },
  },
]);
const RUN = 'run --rm --network none --volumes-from l1:ro grafana/loki:3.7.7 -config.file=/etc/loki/config.yaml.next -verify-config';
const HEALTH = 'exec l1 /usr/bin/loki -health';

/** The whole case body. `rewrite` replaces every arm, so a test flips one by building the list again. */
const arms = (o: { ps?: string; run?: string; health?: 0 | 1 } = {}): string =>
  [
    arm(PS, o.ps ?? 'l1'),
    arm('inspect l1', INSPECT),
    o.run ?? arm(RUN, ''),
    arm('restart l1', 'l1'),
    arm(HEALTH, o.health ? 'Unhealthy' : 'ready', o.health ?? 0),
    arm('logs --since * l1', ''),
  ].join('\n');

const CHUNKS = { idlePeriodMinutes: 30, maxAgeMinutes: 120, targetSizeKiB: 1536, encoding: 'snappy' };

type Step = { axis: string; phase: string; ok: boolean; message?: string };
type Job = {
  id: string;
  stack: string;
  action: string;
  state: string;
  outcome?: { ok: boolean; steps: Step[] };
  log?: Array<{ level: string; message: string }>;
};
type View = {
  enabled: boolean | null;
  source: string;
  updatedAt: number | null;
  retentionDays: number;
  storage: { type: string; s3: Record<string, unknown> | null };
  limits: { earliestCutover: string };
};
type Loki = { s: Booted; shim: Shim; dir: string; stop: () => Promise<void> };

/** A server whose loki directory holds `config`, with a recording shim on PATH. */
async function boot(armText: string, config = DEFAULT): Promise<Loki> {
  const shim = dockerShim(armText, { record: true });
  const dir = tmpd('loki-dir');
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, 'config.yaml'), config);
  const s = await bootServer({
    tag: 'loki-settings',
    pathPrefix: shim.dir,
    env: {
      PSTACK_LOKI_DIR: dir,
      PSTACK_LOKI_READY_TIMEOUT_MS: '4000',
      // Our own uid: the credentials file is then Loki's without a chown.
      PSTACK_LOKI_UID: String(process.getuid?.() ?? 0),
    },
  });
  return {
    s,
    shim,
    dir,
    stop: async () => {
      await s.stop();
      shim.remove();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

const put = (s: Booted, path: string, body: unknown) =>
  fetch(`${s.base}${path}`, { method: 'PUT', headers: s.H, body: JSON.stringify(body) });
const saveRetention = (s: Booted, days: number) => put(s, '/api/logging', { retentionDays: days, chunks: CHUNKS });
const getLogging = async (s: Booted): Promise<View> => (await (await fetch(`${s.base}/api/logging`, { headers: s.H })).json()) as View;
const jobOf = async (r: Response): Promise<string> => ((await r.json()) as { job: { id: string } }).job.id;
const errorOf = async (r: Response): Promise<string> => ((await r.json()) as { error: string }).error;
const restarts = (shim: Shim): number => shim.calls().filter((c) => c === 'restart l1').length;
const messages = (job: Job): Array<string | undefined> => (job.outcome?.steps ?? []).map((st) => st.message);
const configOf = (dir: string): string => readFileSync(join(dir, 'config.yaml'), 'utf8');

/** Until the job is terminal. The harness's waitJob stops at `queued` (server.ts:171), and test 6 queues one. */
async function waitTerminal(s: Booted, id: string, ms = 30_000): Promise<Job> {
  const live = (j?: Job) => !j || j.state === 'queued' || j.state === 'running';
  const job = await until(
    async () => ((await (await fetch(`${s.base}/api/jobs/${id}`, { headers: s.H })).json()) as { job?: Job }).job,
    (j) => !live(j),
    ms,
    50,
  );
  if (live(job)) throw new Error(`job ${id} never finished`);
  return job!;
}

const KEY_ID = 'AKIACONFORMANCE01';
const SECRET = 'conformance-s3-secret-0123456789';
const EMPTY_SHA256 = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
const DENIED =
  '<?xml version="1.0" encoding="UTF-8"?>\n<Error><Code>InvalidAccessKeyId</Code><Message>The AWS Access Key Id you provided does not exist in our records.</Message></Error>';

/** S3 for the probe: PUT answers `state.putStatus`, DELETE answers 204, and every request is recorded. */
function fakeS3() {
  const seen: Array<{ method: string; path: string; auth: string; sha: string }> = [];
  const state = { putStatus: 200 };
  const srv = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    fetch: (req) => {
      seen.push({
        method: req.method,
        path: new URL(req.url).pathname,
        auth: req.headers.get('authorization') ?? '',
        sha: req.headers.get('x-amz-content-sha256') ?? '',
      });
      if (req.method === 'PUT' && state.putStatus !== 200) {
        return new Response(DENIED, { status: state.putStatus, headers: { 'content-type': 'application/xml' } });
      }
      return new Response(null, { status: req.method === 'DELETE' ? 204 : 200 });
    },
  });
  return { seen, state, port: srv.port as number, stop: () => srv.stop(true) };
}

describe('Loki settings over HTTP', () => {
  test('GET on an empty table: the defaults, the limits, enabled', async () => {
    // negative control: tag loggingStorageView.S3 `json:"s3,omitempty"` — `s3` leaves the body and both assertions fail.
    const { s, stop } = await boot(arms());
    try {
      const r = await fetch(`${s.base}/api/logging`, { headers: s.H });
      expect(r.status).toBe(200);
      const v = (await r.json()) as Record<string, unknown>;
      expect(Object.keys(v)).toEqual(['enabled', 'source', 'updatedAt', 'retentionDays', 'chunks', 'storage', 'limits']);
      expect(v).toEqual({
        enabled: true,
        source: 'default',
        updatedAt: null,
        retentionDays: 7,
        chunks: CHUNKS,
        storage: { type: 'filesystem', s3: null },
        limits: {
          retentionDays: { min: 1, max: 365 },
          idlePeriodMinutes: { min: 5, max: 60 },
          maxAgeMinutes: { min: 30, max: 180 },
          targetSizeKiB: { min: 512, max: 1536 },
          encodings: ['snappy', 'gzip', 'lz4', 'zstd'],
          earliestCutover: expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/),
        },
      });
      // Always a later day: a period from today would already be live.
      expect((v.limits as { earliestCutover: string }).earliestCutover > new Date().toISOString().slice(0, 10)).toBe(true);
    } finally {
      await stop();
    }
  }, 20_000);

  test('a retention save renders both keys, restarts Loki once, in order, and reads back as db', async () => {
    // negative control: in loki.Render's anchor slice, make the max_query_lookback entry's replacement the anchor itself — the file keeps `max_query_lookback: 168h`.
    const { s, shim, dir, stop } = await boot(arms());
    try {
      const from = shim.calls().length;
      const r = await saveRetention(s, 14);
      expect(r.status).toBe(202);
      const { job } = (await r.json()) as { job: { id: string; stack: string; action: string } };
      expect(job).toMatchObject({ stack: 'pstack-control', action: 'loki-apply' });
      expect((await waitTerminal(s, job.id)).state).toBe('ok');
      expect(configOf(dir)).toContain('  retention_period: 336h');
      expect(configOf(dir)).toContain('  max_query_lookback: 336h');
      // ps and inspect come before the handler, the find and the restart; only the verbs are pinned exactly.
      const verbs = shim.calls().slice(from).filter((c) => !c.startsWith('ps ') && !c.startsWith('inspect '));
      expect(verbs.slice(0, 3)).toEqual([RUN, 'restart l1', HEALTH]);
      expect(verbs).toHaveLength(4);
      expect(verbs[3]).toMatch(/^logs --since \d{4}-\d{2}-\d{2}T\S+ l1$/);
      const v = await getLogging(s);
      expect(v).toMatchObject({ source: 'db', retentionDays: 14 });
      expect(typeof v.updatedAt).toBe('number');
    } finally {
      await stop();
    }
  }, 30_000);

  test('a config Loki refuses changes nothing: no restart, no .next file, the row untouched', async () => {
    // negative control: in the apply's verify step, write the render to config.yaml instead of config.yaml.next — the file no longer equals the default.
    const LAST = 'failed parsing config: chunk_encoding: unknown codec';
    const refusal = `  ${casePattern(RUN)}) printf '%s\\n' ${sq(`level=info msg="reading config"\n${LAST}`)} >&2; exit 1 ;;`;
    const { s, shim, dir, stop } = await boot(arms({ run: refusal }));
    try {
      const r = await saveRetention(s, 14);
      expect(r.status).toBe(202);
      const done = await waitTerminal(s, await jobOf(r));
      expect(done.state).toBe('failed');
      expect(done.outcome?.steps.find((st) => st.phase === 'verify')).toMatchObject({ ok: false, message: LAST });
      expect(configOf(dir)).toBe(DEFAULT);
      expect(readdirSync(dir)).toEqual(['config.yaml']);
      expect(restarts(shim)).toBe(0);
      expect(await getLogging(s)).toMatchObject({ source: 'default', retentionDays: 7, updatedAt: null });
    } finally {
      await stop();
    }
  }, 30_000);

  test('a Loki that never answers ready is rolled back to the previous file and row', async () => {
    // negative control: in the apply's ready step, return the failed step on a timeout without calling a.rollback — one restart, config.yaml keeps 336h, no `rolled back`.
    const { s, shim, dir, stop } = await boot(arms({ health: 1 }));
    try {
      const r = await saveRetention(s, 14);
      expect(r.status).toBe(202);
      const id = await jobOf(r);
      // The first ready wait times out after 4s; the second restart is the rollback's. Let it come up.
      await until(async () => restarts(shim), (n) => n >= 2, 20_000);
      shim.rewrite(arms());
      const done = await waitTerminal(s, id);
      expect(done.state).toBe('failed');
      expect(messages(done)).toContain('rolled back');
      expect(configOf(dir)).toBe(DEFAULT);
      expect(restarts(shim)).toBe(2);
      expect(await getLogging(s)).toMatchObject({ source: 'default', retentionDays: 7, updatedAt: null });
    } finally {
      await stop();
    }
  }, 60_000);

  test('refusals: an unchanged save is 200, out of range is 400, no loki container is 409 before the body', async () => {
    // negative control: in lokiSave, move step 3 (parse, loki.Read, loki.Merge) above the ControlRuntime check — the out-of-range save on the loki-less host answers 400.
    const { s, shim, stop } = await boot(arms());
    try {
      const same = await saveRetention(s, 7);
      expect(same.status).toBe(200);
      expect(await same.json()).toEqual({ changed: false });
      expect(shim.calls().filter((c) => c === RUN || c === 'restart l1')).toEqual([]);

      const bad = { retentionDays: 0, chunks: CHUNKS };
      const low = await put(s, '/api/logging', bad);
      expect(low.status).toBe(400);
      expect(await errorOf(low)).toBe('retentionDays must be 1–365');

      shim.rewrite(arms({ ps: '' }));
      expect((await getLogging(s)).enabled).toBe(false);
      const gone = await put(s, '/api/logging', bad);
      expect(gone.status).toBe(409);
      expect(await errorOf(gone)).toBe('Loki is not running on this host — run pstack logging loki on the host');
    } finally {
      await stop();
    }
  }, 30_000);

  test('a revert saved during an apply is queued as a job, never answered as a no-op', async () => {
    // negative control: in loggingPut's no-op step, delete `!s.jobs.IsBusy(inspect.ControlProject) && ` — the revert equals the still-empty row and answers 200.
    // A 2s verify holds job A after it took its patch and before it commits.
    const slow = `  ${casePattern(RUN)}) sleep 2; exit 0 ;;`;
    const { s, shim, dir, stop } = await boot(arms({ run: slow, health: 1 }));
    try {
      const first = await saveRetention(s, 14);
      expect(first.status).toBe(202);
      const a = await jobOf(first);
      await until(async () => shim.calls().includes(RUN), (seen) => seen, 10_000);
      const second = await saveRetention(s, 7);
      expect(second.status).toBe(202);
      const b = await jobOf(second);
      expect(b).not.toBe(a);
      // A commits 14, never goes ready, and rolls back: the second restart is the rollback's.
      await until(async () => restarts(shim), (n) => n >= 2, 30_000);
      shim.rewrite(arms());
      const doneA = await waitTerminal(s, a);
      expect(doneA.state).toBe('failed');
      expect(messages(doneA)).toContain('rolled back');
      // B runs on the reverted row: 7 is the default, and the file already is the default render.
      const doneB = await waitTerminal(s, b);
      expect(doneB.state).toBe('ok');
      expect(messages(doneB)).toContain('nothing to change');
      expect(restarts(shim)).toBe(2);
      expect(await getLogging(s)).toMatchObject({ source: 'default', retentionDays: 7 });
      expect(configOf(dir)).toBe(DEFAULT);
    } finally {
      await stop();
    }
  }, 60_000);

  test('S3: a refused probe is 400, a save writes 0600 credentials, the secret has no read path, S3 is one-way', async () => {
    // negative control: in loki.Probe, append the response body to the PUT refusal error — the 400 carries `does not exist in our records`.
    const s3 = fakeS3();
    const { s, shim, dir, stop } = await boot(arms());
    try {
      const { limits } = await getLogging(s);
      const cutover = limits.earliestCutover;
      const body = {
        type: 's3',
        endpoint: `http://127.0.0.1:${s3.port}`,
        region: 'us-east-1',
        bucket: 'pstack-logs',
        pathStyle: true,
        accessKeyId: KEY_ID,
        secretAccessKey: SECRET,
        cutover,
      };
      const creds = join(dir, 's3-credentials');

      s3.state.putStatus = 403;
      const refused = await put(s, '/api/logging/storage', body);
      expect(refused.status).toBe(400);
      const said = await refused.text();
      expect((JSON.parse(said) as { error: string }).error).toBe('S3 refused the probe: 403 InvalidAccessKeyId');
      expect(said).not.toContain('does not exist in our records');
      expect(s3.seen.map((r) => r.method)).toEqual(['PUT']);
      expect(existsSync(creds)).toBe(false);

      s3.state.putStatus = 200;
      const saved = await put(s, '/api/logging/storage', body);
      expect(saved.status).toBe(202);
      expect((await waitTerminal(s, await jobOf(saved))).state).toBe('ok');
      // The probe: PUT then DELETE of one key, SigV4-signed over an empty body.
      expect(s3.seen).toHaveLength(3);
      const [probe, cleanup] = s3.seen.slice(1);
      expect(probe).toMatchObject({ method: 'PUT', sha: EMPTY_SHA256 });
      expect(probe!.path).toMatch(/^\/pstack-logs\/pstack-probe-[0-9a-f]{16}$/);
      expect(probe!.auth).toStartWith(`AWS4-HMAC-SHA256 Credential=${KEY_ID}/`);
      expect(cleanup).toMatchObject({ method: 'DELETE', path: probe!.path });

      const cfg = configOf(dir);
      expect(cfg).toContain(`    - from: "${cutover}"`);
      expect(cfg).toContain('      object_store: s3\n');
      expect(cfg).toContain(`    endpoint: 127.0.0.1:${s3.port}\n`);
      expect(cfg).toContain('    insecure: true\n');
      expect(cfg).toContain('  delete_request_store: s3');
      expect(cfg).not.toContain(SECRET);
      expect((statSync(creds).mode & 0o777).toString(8)).toBe('600');
      expect(readFileSync(creds, 'utf8')).toBe(`[default]\naws_access_key_id = ${KEY_ID}\naws_secret_access_key = ${SECRET}\n`);

      // No read path: not the secret, not a mask of any length.
      const read = await (await fetch(`${s.base}/api/logging`, { headers: s.H })).text();
      expect(read).not.toContain(SECRET);
      expect(read).not.toContain('•');
      expect((JSON.parse(read) as View).storage).toEqual({
        type: 's3',
        s3: { endpoint: body.endpoint, region: 'us-east-1', bucket: 'pstack-logs', pathStyle: true, accessKeyId: KEY_ID, secretSet: true, cutover },
      });

      // The mask keeps the stored secret. Every other field is fixed, so it resolves to the row: 200, no probe.
      const masked = await put(s, '/api/logging/storage', { ...body, secretAccessKey: '••••••••' });
      expect(masked.status).toBe(200);
      expect(await masked.json()).toEqual({ changed: false });
      expect(readFileSync(creds, 'utf8')).toContain(`aws_secret_access_key = ${SECRET}\n`);
      expect(s3.seen).toHaveLength(3);

      const back = await put(s, '/api/logging/storage', { type: 'filesystem' });
      expect(back.status).toBe(409);
      expect(await errorOf(back)).toBe('S3 is one-way on this host');
      const moved = await put(s, '/api/logging/storage', { ...body, bucket: 'pstack-logs-2' });
      expect(moved.status).toBe(409);
      expect(await errorOf(moved)).toBe('S3 storage is fixed once saved — only accessKeyId and secretAccessKey change');
      expect(restarts(shim)).toBe(1);
    } finally {
      await stop();
      s3.stop();
    }
  }, 60_000);

  test('boot renders a drifted config.yaml back through one loki-apply job', async () => {
    // negative control: delete the s.reconcileLoki() call from api.New — no loki-apply job is listed and the file keeps 72h.
    const drifted = DEFAULT.replace('  retention_period: 168h', '  retention_period: 72h');
    expect(drifted).not.toBe(DEFAULT);
    const { s, shim, dir, stop } = await boot(arms(), drifted);
    try {
      const { jobs } = (await (await fetch(`${s.base}/api/jobs`, { headers: s.H })).json()) as { jobs: Job[] };
      const applies = jobs.filter((j) => j.action === 'loki-apply');
      expect(applies.map((j) => j.stack)).toEqual(['pstack-control']);
      const done = await waitTerminal(s, applies[0]!.id);
      expect(done.state).toBe('ok');
      expect((done.log ?? []).map((e) => e.message)).toContain('by pstack (boot)');
      expect(configOf(dir)).toBe(DEFAULT);
      expect(restarts(shim)).toBe(1);
    } finally {
      await stop();
    }
  }, 30_000);
});
