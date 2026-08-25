/**
 * THE TWO RUNTIME HOST SETTINGS, proved from outside the binary: `max_jobs` and `default_role`.
 *
 * `internal/settings` and `internal/api` have their own tests for the resolver and the routes. This
 * file asserts the three things only a real process can show, and they are the three an operator
 * actually depends on:
 *
 *   1. PRECEDENCE ACROSS A RESTART — database > environment > built-in default. The stored value has
 *      to still be the answer after the container comes back with `PSTACK_MAX_JOBS` still saying
 *      something else. An in-process test cannot see this: it is the boot path, and the whole point
 *      of moving the cap out of the environment is that a restart does not undo the UI.
 *   2. THE CAP APPLIES WITHOUT A RESTART, SAMPLED. "Only one job ran" is not inferrable from four
 *      finished jobs — every ordering ends the same way. So the job list is POLLED while the jobs
 *      are in flight and every sample is asserted, which is the only observation that can tell a
 *      real cap from a queue that happened to drain in order.
 *   3. LOWERING IT KILLS NOTHING. An operator who types 1 while three jobs run must find three jobs
 *      still running; the new cap applies to the next dispatch, and the fourth job proves it did
 *      apply rather than being ignored.
 *
 * Plus the two places a role comes from a setting instead of a constant: `POST /api/users` with no
 * `role`, and an SSO provider whose `defaultRole` is EMPTY — which now means "inherit the host
 * default", resolved at provision time. That one is driven through the real fake IdP because it is
 * the regression this whole area exists to prevent: empty used to mean `admin`, and with every
 * allow-list empty (how every preset saves) a stranger completing the OAuth flow was minted a full
 * administrator. Every assertion here therefore also pins that omission never yields admin.
 *
 * A docker shim is on PATH for the job tests so a machine with docker, without it, or running a
 * swarm all schedule the same way — the jobs are axis hooks (`sleep`), and nothing here depends on
 * a container existing.
 */
import { describe, expect, test } from 'bun:test';
import { ALWAYS_OK, dockerShim } from '../harness/docker-shim.ts';
import { fakeProvider, type FakeProvider } from '../harness/idp.ts';
import { bootServer, tmpd, until, type Booted } from '../harness/server.ts';

const PASSWORD = 'correct-horse-battery-staple';

type SettingRow = { key: string; value: number | string; source: string; minRole: string };
type SettingsBody = { settings: SettingRow[]; env: { PSTACK_MAX_JOBS: number | null }; precedence: string };
type JobRow = { id: string; stack: string; action: string; state: string };

const readSettings = async (s: Booted, headers = s.H): Promise<SettingsBody> =>
  (await (await fetch(`${s.base}/api/settings`, { headers })).json()) as SettingsBody;

const putSetting = (s: Booted, key: string, value: unknown, headers = s.H) =>
  fetch(`${s.base}/api/settings/${key}`, { method: 'PUT', headers, body: JSON.stringify({ value }) });

/** Every job the registry is holding. Throws if the answer is not the documented shape. */
const jobsOf = async (s: Booted): Promise<JobRow[]> => {
  const body = (await (await fetch(`${s.base}/api/jobs`, { headers: s.H })).json()) as { jobs: JobRow[] };
  return body.jobs.map((j) => ({ id: j.id, stack: j.stack, action: j.action, state: j.state }));
};

const inState = (jobs: JobRow[], state: string): JobRow[] => jobs.filter((j) => j.state === state);

/**
 * "at most `cap` running", written as a string comparison against a clamped copy of itself.
 *
 * `toBeLessThanOrEqual` would report "expected <= 1, received 3" and leave you guessing WHICH sample
 * broke; this prints the sample number and the count in the same line. It cannot pass vacuously on
 * its own — zero running satisfies it — which is why every sampling loop below also asserts that it
 * SAW the state it is bounding.
 */
const atMostRunning = (label: string, jobs: JobRow[], cap: number): void => {
  const n = inState(jobs, 'running').length;
  expect(`${label}: ${n} running`).toBe(`${label}: ${Math.min(n, cap)} running`);
};

/** A deployment whose `up` is one long-running axis hook — a job that stays `running` to be sampled. */
async function holdingDeployment(s: Booted, id: string, seconds: number): Promise<void> {
  const r = await fetch(`${s.base}/api/deployments/${id}`, {
    method: 'PUT',
    headers: s.H,
    body: JSON.stringify({ spec: `version: 1\nstack: ${id}\naxes:\n  - name: hold\n    up: "sleep ${seconds}"\n` }),
  });
  expect(`${id} → ${r.status}`).toBe(`${id} → 201`);
}

/** Start `up` and hand back the job id. */
async function startUp(s: Booted, id: string): Promise<string> {
  const r = await fetch(`${s.base}/api/deployments/${id}/up`, { method: 'POST', headers: s.H });
  expect(`${id} up → ${r.status}`).toBe(`${id} up → 202`);
  return ((await r.json()) as { job: { id: string } }).job.id;
}

const createUser = async (s: Booted, body: Record<string, unknown>, headers = s.H) => {
  const r = await fetch(`${s.base}/api/users`, { method: 'POST', headers, body: JSON.stringify(body) });
  return { status: r.status, body: (await r.json()) as { user?: { username: string; role: string }; error?: string } };
};

/** Sign in and return the headers that authenticate as that account. */
async function session(base: string, username: string): Promise<Record<string, string>> {
  const login = await fetch(`${base}/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ username, password: PASSWORD }),
  });
  const raw = login.headers.get('set-cookie');
  if (!raw) throw new Error(`login as ${username} returned ${login.status} and no session cookie`);
  return { cookie: raw.split(';')[0]!, 'content-type': 'application/json' };
}

describe('settings: max_jobs and default_role, at runtime', () => {
  // negative control: in internal/settings/settings.go, MaxJobs() checks s.envMaxJobs BEFORE the
  // stored row — the write reads back `"value": 2` where the test wants 6, so the database stops
  // outranking the environment before the restart is even reached. Also: in routes_settings.go emit
  // `s.opts.MaxJobs` unconditionally in the `env` block — the bare host reports
  // `"PSTACK_MAX_JOBS": 0`, which is indistinguishable from someone having set it to a value this
  // binary ignored. Both run, observed failing, restored.
  test('precedence is database > environment > built-in default — and the database survives a restart', async () => {
    // ── layer three: nothing stored, nothing in the environment ────────────────────────────────
    const bare = await bootServer({ tag: 'set-bare' });
    try {
      expect(await readSettings(bare)).toEqual({
        settings: [
          { key: 'max_jobs', value: 4, source: 'default', minRole: 'maintainer' },
          { key: 'default_role', value: 'viewer', source: 'default', minRole: 'admin' },
        ],
        // NULL, not 0: `PSTACK_MAX_JOBS=0` is read as unset, so a literal 0 could not be told apart
        // from "never set" and would contradict the row's own `source: "default"`.
        env: { PSTACK_MAX_JOBS: null },
        precedence: 'database > environment > built-in default',
      });
    } finally {
      await bare.stop();
    }

    // ── layer two: the environment is the DEFAULT, and it says so ───────────────────────────────
    const dataDir = tmpd('set-precedence');
    const env = { PSTACK_MAX_JOBS: '2' };
    const first = await bootServer({ tag: 'set-env', dataDir, env, keep: true });
    try {
      expect(await readSettings(first)).toMatchObject({
        settings: [{ key: 'max_jobs', value: 2, source: 'env', minRole: 'maintainer' }, { key: 'default_role', value: 'viewer', source: 'default', minRole: 'admin' }],
        env: { PSTACK_MAX_JOBS: 2 },
      });

      // ── layer one: a write outranks it, and the answer says which layer won ───────────────────
      const wrote = await putSetting(first, 'max_jobs', 6);
      expect(wrote.status).toBe(200);
      const row = (await wrote.json()) as SettingRow & { stored: boolean; note: string };
      expect(row).toMatchObject({ key: 'max_jobs', value: 6, source: 'db', minRole: 'maintainer', stored: true });
      // The 200 says, in words, what lowering the cap does NOT do — see the third test.
      expect(row.note).toContain('run to completion');
      expect(row.note).toContain('cancelled nothing');

      const after = await readSettings(first);
      expect(after.settings[0]).toEqual({ key: 'max_jobs', value: 6, source: 'db', minRole: 'maintainer' });
      // The environment did not change and is still reported — that is what makes a 6 explicable.
      expect(after.env).toEqual({ PSTACK_MAX_JOBS: 2 });
    } finally {
      await first.stop();
    }

    // ── THE POINT: restart with the same environment still saying 2 ─────────────────────────────
    const second = await bootServer({ tag: 'set-restart', dataDir, env });
    try {
      const back = await readSettings(second);
      expect(back.settings[0]).toEqual({ key: 'max_jobs', value: 6, source: 'db', minRole: 'maintainer' });
      expect(back.env).toEqual({ PSTACK_MAX_JOBS: 2 });
    } finally {
      await second.stop();
    }
  }, 60_000);

  // negative control: in internal/api/server.go's New, hand jobs.New the boot option again
  // (`jobs.New(o.Bus, o.MaxJobs)`) instead of the resolved cap — both jobs run and the sampled
  // "only one at a time" assertion fires. Run, observed failing, restored.
  test('a stored cap is in force AT BOOT, before anyone opens the settings page', async () => {
    const shim = dockerShim(ALWAYS_OK);
    const dataDir = tmpd('set-boot');
    // PSTACK_MAX_JOBS says 4; the stored row is going to say 1, and the boot must take the row.
    const env = { PSTACK_MAX_JOBS: '4' };
    const seed = await bootServer({ tag: 'set-boot-seed', dataDir, env, keep: true });
    try {
      expect((await putSetting(seed, 'max_jobs', 1)).status).toBe(200);
    } finally {
      await seed.stop();
    }

    const s = await bootServer({ tag: 'set-boot-run', dataDir, env, pathPrefix: shim.dir });
    try {
      expect((await readSettings(s)).settings[0]).toMatchObject({ value: 1, source: 'db' });
      for (const id of ['boot-a', 'boot-b']) await holdingDeployment(s, id, 5);
      await startUp(s, 'boot-a');
      await startUp(s, 'boot-b');

      // Sampled while both are outstanding: one runs, the other waits for the slot.
      let sawTheWait = false;
      for (let i = 0; i < 20; i++) {
        const jobs = await jobsOf(s);
        atMostRunning(`sample ${i}`, jobs, 1);
        if (inState(jobs, 'running').length === 1 && inState(jobs, 'queued').length === 1) sawTheWait = true;
        await Bun.sleep(60);
      }
      expect(sawTheWait).toBe(true);
    } finally {
      await s.stop();
      shim.remove();
    }
  }, 60_000);

  // negative control: in internal/api/routes_settings.go drop the `s.jobs.SetMaxRunning(...)` call
  // from putSetting — the PUT never reaches the registry, the first sample is "3 running of 3", and
  // the cap is decoration. Also: in internal/jobs/jobs.go drop `r.pump()` from SetMaxRunning — the
  // cap of 1 still holds but the RAISE dispatches nothing, and the two ids that were queued are
  // still queued five seconds later. Both run, observed failing, restored.
  test('the cap applies to a RUNNING server: one at a time, then a raise dispatches what was waiting', async () => {
    const shim = dockerShim(ALWAYS_OK);
    const s = await bootServer({ tag: 'set-cap', pathPrefix: shim.dir });
    try {
      expect((await putSetting(s, 'max_jobs', 1)).status).toBe(200);
      const ids: string[] = [];
      for (const id of ['cap-a', 'cap-b', 'cap-c']) {
        await holdingDeployment(s, id, 8);
        ids.push(await startUp(s, id));
      }

      // THREE STACKS, ONE SLOT — sampled, because three finished jobs look identical whatever the
      // cap was. Every sample must hold, not just the last one.
      for (let i = 0; i < 20; i++) {
        atMostRunning(`sample ${i} of ${ids.length} stacks`, await jobsOf(s), 1);
        await Bun.sleep(60);
      }

      // ONE snapshot decides what the raise has to move: who holds the only slot, and who is
      // waiting for one. Taken here rather than accumulated over the loop so there is exactly one
      // moment this depends on, well inside the hooks' eight seconds.
      const before = await jobsOf(s);
      const holder = inState(before, 'running').map((j) => j.id);
      const waiting = inState(before, 'queued').map((j) => j.id);
      expect(`${holder.length} running, ${waiting.length} waiting`).toBe('1 running, 2 waiting');

      // RAISE IT. The two jobs that were waiting for a slot must start — on this request, without a
      // restart, and while the first one is still holding its own slot (so nothing was freed by a
      // job finishing).
      expect((await putSetting(s, 'max_jobs', 3)).status).toBe(200);
      const now = await until(() => jobsOf(s), (jobs) => waiting.every((id) => jobs.find((j) => j.id === id)?.state === 'running'), 5000);
      expect(waiting.map((id) => `${id}: ${now.find((j) => j.id === id)?.state}`)).toEqual(waiting.map((id) => `${id}: running`));
      // Nothing was freed to make room: the job that held the only slot is still holding it, so all
      // three ids are running at once.
      expect(`${holder[0]}: ${now.find((j) => j.id === holder[0])?.state}`).toBe(`${holder[0]}: running`);
      expect(inState(now, 'running').map((j) => j.id).sort()).toEqual([...ids].sort());
    } finally {
      await s.stop();
      shim.remove();
    }
  }, 90_000);

  // negative control: in internal/jobs/jobs.go make SetMaxRunning cancel the excess (after
  // unlocking, `r.Cancel` every running job past n) — sample 1 reads "1 still running" where the
  // test wants 3. Also: make it a no-op for a LOWER n (`if n > r.maxRunning` around the
  // assignment) — nothing is killed, so the sampling still passes, and the fourth job comes back
  // `running` instead of `queued`: the PUT was cosmetic. Both run, observed failing, restored.
  test('lowering the cap cancels nothing that is already running — it applies to the next dispatch', async () => {
    const shim = dockerShim(ALWAYS_OK);
    const s = await bootServer({ tag: 'set-lower', pathPrefix: shim.dir });
    try {
      expect((await putSetting(s, 'max_jobs', 3)).status).toBe(200);
      const ids: string[] = [];
      for (const id of ['low-a', 'low-b', 'low-c']) {
        await holdingDeployment(s, id, 8);
        ids.push(await startUp(s, id));
      }
      await until(() => jobsOf(s), (jobs) => inState(jobs, 'running').length === 3, 5000);

      // ONE, while three run. Nothing is killed to make the number fit.
      const lowered = await putSetting(s, 'max_jobs', 1);
      expect(lowered.status).toBe(200);
      expect(((await lowered.json()) as { note: string }).note).toContain('run to completion');

      for (let i = 0; i < 12; i++) {
        const jobs = await jobsOf(s);
        expect(`sample ${i}: ${inState(jobs, 'running').length} still running`).toBe(`sample ${i}: 3 still running`);
        await Bun.sleep(60);
      }

      // But it DID take effect: the next job to be accepted waits, and it waits behind three jobs
      // that a cap of 1 would never have started. Without this the test above passes against a PUT
      // the registry ignored entirely.
      await holdingDeployment(s, 'low-d', 8);
      const fourth = await startUp(s, 'low-d');
      const withFourth = await jobsOf(s);
      expect(`${fourth}: ${withFourth.find((j) => j.id === fourth)?.state}`).toBe(`${fourth}: queued`);

      // And the three ran to completion — not one of them is `cancelled`.
      const finished = await until(() => jobsOf(s), (jobs) => ids.every((id) => !['running', 'queued'].includes(jobs.find((j) => j.id === id)?.state ?? '')), 30_000);
      expect(ids.map((id) => `${id}: cancelled? ${finished.find((j) => j.id === id)?.state === 'cancelled'}`)).toEqual(ids.map((id) => `${id}: cancelled? false`));
    } finally {
      await s.stop();
      shim.remove();
    }
  }, 90_000);

  // negative control: in internal/api/routes_auth.go put `role := string(auth.Viewer)` back in the
  // users POST — "after" comes back a viewer though the setting says developer. Also: in
  // internal/auth/auth.go give Bootstrap a role (`CreateOpts{Role: string(Developer)}`, standing in
  // for it being wired to the setting) — the first account on the host is a developer and the
  // bootstrap assertion fires. Both run, observed failing, restored.
  test('POST /api/users with no role takes the host default, and an explicit role still wins', async () => {
    const s = await bootServer({ tag: 'set-role' });
    try {
      // Nobody set one: the least privilege, exactly as before this feature existed.
      expect((await createUser(s, { username: 'before', password: PASSWORD })).body.user).toMatchObject({ username: 'before', role: 'viewer' });

      expect((await putSetting(s, 'default_role', 'developer')).status).toBe(200);
      expect((await readSettings(s)).settings[1]).toEqual({ key: 'default_role', value: 'developer', source: 'db', minRole: 'admin' });

      // The NEXT account created without a role is a developer — the setting is read at creation
      // time, and it changed what an unchanged request produces.
      expect((await createUser(s, { username: 'after', password: PASSWORD })).body.user).toMatchObject({ username: 'after', role: 'developer' });
      // A named role is still a named role.
      expect((await createUser(s, { username: 'named', password: PASSWORD, role: 'viewer' })).body.user).toMatchObject({ username: 'named', role: 'viewer' });
      // And an unknown one is still refused rather than stored — the setting did not open a door.
      const bogus = await createUser(s, { username: 'superuser-please', password: PASSWORD, role: 'superuser' });
      expect(bogus.status).toBe(400);
      expect(bogus.body).toEqual({ error: 'role must be one of: viewer, developer, maintainer, admin' });
    } finally {
      await s.stop();
    }

    // THE FIRST ACCOUNT ON A HOST IS AN ADMIN WHATEVER THE SETTING SAYS. There is nobody to promote
    // it, so a host whose default_role was lowered before bootstrap must not end up with zero admins.
    const fresh = await bootServer({ tag: 'set-bootstrap' });
    try {
      expect((await putSetting(fresh, 'default_role', 'developer')).status).toBe(200);
      const r = await fetch(`${fresh.base}/api/auth/bootstrap`, { method: 'POST', headers: fresh.H, body: JSON.stringify({ username: 'first', password: PASSWORD }) });
      expect(r.status).toBe(201);
      expect(((await r.json()) as { user: { username: string; role: string } }).user).toMatchObject({ username: 'first', role: 'admin' });
    } finally {
      await fresh.stop();
    }
  }, 60_000);

  // negative control: in internal/sso/sso.go put the fill back into ParseConfig
  // (`if cfg.DefaultRole == "" { cfg.DefaultRole = "viewer" }`) — the save reads back
  // `"defaultRole": "viewer"`, the answer frozen into the row, so bob would keep being minted a
  // viewer after the host default moved. Then the SAME fill with "admin" — the read-back is
  // `"defaultRole": "admin"`, which is the exact regression this test exists for: every allow-list
  // is empty, so that is a full administrator for anyone who completes the flow. Also: in
  // internal/api/routes_auth.go make ssoSignInOpts return cfg.DefaultRole unresolved — bob is
  // minted a viewer where the host default says maintainer, because nothing resolved "inherit".
  // All three run, observed failing, restored.
  test('an SSO provider with an EMPTY defaultRole inherits the host default — never admin by omission', async () => {
    const p = await fakeProvider();
    const s = await bootServer({ tag: 'set-sso', sso: { stateTtlS: 300 } });
    try {
      const saved = await fetch(`${s.base}/api/sso/config`, {
        method: 'PUT',
        headers: s.H,
        body: JSON.stringify({
          mode: 'oauth2',
          provider: 'custom',
          label: 'Corp',
          clientId: 'cid',
          clientSecret: 'shh',
          authorizeUrl: `${p.base}/authorize`,
          tokenUrl: `${p.base}/token`,
          userInfoUrl: `${p.base}/userinfo`,
          claimMap: { subject: 'id', username: 'login', email: 'email', name: 'name' },
          // EMPTY, on purpose. This used to be refused as invalid, and before that filled in.
          defaultRole: '',
        }),
      });
      expect(saved.status).toBe(200);
      expect((await saved.json()) as { config: { defaultRole: string } }).toMatchObject({ config: { defaultRole: '' } });
      // Stored as typed: the answer was NOT frozen into the row at save time, which is what makes
      // the host default able to move it later.
      const read = (await (await fetch(`${s.base}/api/sso/config`, { headers: s.H })).json()) as { providers: { config: { defaultRole: string } }[] };
      expect(read.providers.map((r) => r.config.defaultRole)).toEqual(['']);

      // ── inherit, with nobody having set a host default: viewer ────────────────────────────────
      const one = await ssoLogin(s, p, { id: 101, login: 'alice', email: 'alice@example.com' });
      expect(one).toMatchObject({ username: 'alice', role: 'viewer' });

      // ── the host default moves, and so does what the SAME provider mints ──────────────────────
      expect((await putSetting(s, 'default_role', 'maintainer')).status).toBe(200);
      const two = await ssoLogin(s, p, { id: 202, login: 'bob', email: 'bob@example.com' });
      // A different subject AND a different verified address: the same email would let this login
      // ADOPT alice's account (invariant 19) and inherit her role, which would pass for the wrong
      // reason. Assert whose account it is before asserting its role.
      expect(two.username).toBe('bob');
      expect(two.role).toBe('maintainer');
      // Alice was not retro-promoted: a role is decided when the account is created.
      const users = (await (await fetch(`${s.base}/api/users`, { headers: s.H })).json()) as { users: { username: string; role: string }[] };
      expect(users.users.map((u) => `${u.username}:${u.role}`).sort()).toEqual(['alice:viewer', 'bob:maintainer']);

      // ── and a provider that NAMES a role still names it: inheriting is opt-in by omission ─────
      const named = await fetch(`${s.base}/api/sso/config`, {
        method: 'PUT',
        headers: s.H,
        body: JSON.stringify({
          mode: 'oauth2',
          provider: 'custom',
          label: 'Corp',
          clientId: 'cid',
          clientSecret: 'shh',
          authorizeUrl: `${p.base}/authorize`,
          tokenUrl: `${p.base}/token`,
          userInfoUrl: `${p.base}/userinfo`,
          claimMap: { subject: 'id', username: 'login', email: 'email', name: 'name' },
          defaultRole: 'developer',
        }),
      });
      expect(named.status).toBe(200);
      const three = await ssoLogin(s, p, { id: 303, login: 'carol', email: 'carol@example.com' });
      expect(three).toMatchObject({ username: 'carol', role: 'developer' });

      // THE ONE THAT MATTERS: not one of these three arrived as an administrator, and the host has
      // no admin account at all — omission never promoted anybody.
      const all = (await (await fetch(`${s.base}/api/users`, { headers: s.H })).json()) as { users: { username: string; role: string }[] };
      expect(all.users.filter((u) => u.role === 'admin')).toEqual([]);
    } finally {
      await s.stop();
      p.stop();
    }
  }, 60_000);

  // negative control: in internal/api/permissions.go set the `/api/settings/default_role` row's min
  // to auth.Maintainer — the read reports `default_role: maintainer` (the row IS what the read
  // shows, which is the point of taking minRole from the table) and the maintainer's refusal is
  // gone one line later. Also: give permit an early `return true` for paths under
  // "/api/settings/" — the table is untouched, so the reported minRole still says admin while the
  // maintainer's write returns 200: the gate and the label disagree, which is exactly the drift
  // this pins. Both run, observed failing, restored.
  test('the tiers are per KEY: a maintainer may cap the host, and may not decide who new accounts are', async () => {
    const s = await bootServer({ tag: 'set-tiers' });
    try {
      const as: Record<string, Record<string, string>> = { root: s.H };
      for (const role of ['viewer', 'developer', 'maintainer', 'admin'] as const) {
        expect((await createUser(s, { username: role, password: PASSWORD, role })).status).toBe(201);
        as[role] = await session(s.base, role);
      }

      // READING is a viewer's — the page that shows the cap is the page everybody sees, and the row
      // itself carries the role it takes to change it.
      const seen = await readSettings(s, as.viewer!);
      expect(seen.settings.map((r) => `${r.key}: ${r.minRole}`)).toEqual(['max_jobs: maintainer', 'default_role: admin']);

      // max_jobs is OPERATIONAL — it sits with host configuration, at maintainer.
      expect((await putSetting(s, 'max_jobs', 2, as.developer!)).status).toBe(403);
      const capped = await putSetting(s, 'max_jobs', 2, as.maintainer!);
      expect(capped.status).toBe(200);
      expect((await capped.json()) as SettingRow).toMatchObject({ key: 'max_jobs', value: 2, source: 'db' });

      // default_role is USER MANAGEMENT by another name — it decides the role of every account
      // created without one, so it sits with the promotion paths, at admin.
      const refused = await putSetting(s, 'default_role', 'admin', as.maintainer!);
      expect(refused.status).toBe(403);
      expect((await refused.json()) as { error: string }).toEqual({ error: 'this route requires the admin role or higher — you are a maintainer' });
      const set = await putSetting(s, 'default_role', 'developer', as.admin!);
      expect(set.status).toBe(200);
      expect((await set.json()) as SettingRow).toMatchObject({ key: 'default_role', value: 'developer', source: 'db' });

      // A key nobody listed has no row in the table, so it is the root token's by default-deny —
      // and even for root it is not a route: the chain dispatches two literals and nothing else.
      const unlisted = await putSetting(s, 'shell_command', 'rm -rf /', as.admin!);
      expect(unlisted.status).toBe(403);
      expect((await unlisted.json()) as { error: string }).toEqual({ error: 'this route is restricted to the PSTACK_TOKEN bearer' });
      expect((await putSetting(s, 'shell_command', 'rm -rf /')).status).toBe(404);
    } finally {
      await s.stop();
    }
  }, 60_000);
});

/**
 * One whole SSO login as a browser would do it, and the account it produced.
 *
 * Each caller passes a DISTINCT subject and a distinct address: an SSO identity is
 * `(providerKey, subject)` and a repeated verified email would ADOPT the earlier account rather than
 * create one (invariant 19), which would make a role assertion pass for the wrong reason.
 */
async function ssoLogin(s: Booted, p: FakeProvider, who: { id: number; login: string; email: string }): Promise<{ username: string; role: string }> {
  p.userInfo = { id: who.id, login: who.login, email: who.email, name: who.login };
  const started = await fetch(`${s.base}/api/auth/sso/start`, { redirect: 'manual' });
  expect(`start ${who.login} → ${started.status}`).toBe(`start ${who.login} → 302`);
  const to = new URL(started.headers.get('location')!);
  p.expectChallenge = to.searchParams.get('code_challenge')!;

  const cb = await fetch(`${s.base}/api/auth/sso/callback?code=abc&state=${encodeURIComponent(to.searchParams.get('state')!)}`, { redirect: 'manual' });
  expect(`callback ${who.login} → ${cb.status}`).toBe(`callback ${who.login} → 302`);
  // Not `/login?sso_error=…`: a refused login also redirects, and would otherwise read as success.
  expect(`callback ${who.login} → ${cb.headers.get('location')}`).toBe(`callback ${who.login} → /`);
  const cookie = cb.headers.get('set-cookie')!.split(';')[0]!;
  const me = (await (await fetch(`${s.base}/api/auth/me`, { headers: { cookie } })).json()) as { user: { username: string; role: string } };
  return { username: me.user.username, role: me.user.role };
}
