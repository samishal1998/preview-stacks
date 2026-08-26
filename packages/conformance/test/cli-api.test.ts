/**
 * `pstack api …` — the generated command tree, driven against a real server.
 *
 * The Go tests prove the tree matches its lock file and that the refusals name the right variable.
 * What only a black-box run can show is that a generated command actually reaches the route it
 * claims: the path is templated correctly, the flags land where the schema said, the bearer token
 * is attached, and a non-2xx becomes a non-zero exit rather than a printed error and exit 0.
 *
 * Those four are the difference between "the commands exist" and "the commands work", and a
 * hand-written CLI would need the same test — the generator does not make it unnecessary, it makes
 * it cheap to keep true for sixty-nine commands instead of the two anyone would bother checking.
 */
import { describe, expect, test } from 'bun:test';
import { NO_CLI } from '../harness/impl.ts';
import { runCli } from '../harness/cli.ts';
import { bootServer, type Booted } from '../harness/server.ts';

/** The environment `pstack api` reads: where the host is, and who we are. */
const apiEnv = (s: Booted) => ({ PSTACK_API_URL: s.base, PSTACK_TOKEN: s.token ?? undefined });

const SPEC = 'version: 1\nstack: cli-api\ncompose: {file: compose.yml, profiles: []}\naxes: []\n';

/**
 * `PUT /api/deployments/:id` takes `--data`, not `--spec`.
 *
 * Its body carries `env`, a map, so the body is not FLAT — and oascmd only derives per-property
 * flags from a flat object (every top-level property a scalar or an array of scalars). That is the
 * library's rule and the right one: there is no sane flag shape for an arbitrary map. Asserted
 * here by using it, so a future spec change that accidentally flattens the body is noticed by a
 * test rather than by a user whose `--data` stopped working.
 */
const putArgs = (id: string) => ['api', 'deployments', 'put', '--id', id, '--data', JSON.stringify({ spec: SPEC })];

describe.skipIf(NO_CLI)('pstack api', () => {
  test('a generated command reaches its route and prints the same JSON the API served', async () => {
    // negative control: point BaseURL at the wrong host — the ids below never match, and the two
    // sources cannot agree by accident because the deployment is created in this test.
    const s = await bootServer({ tag: 'cli-api' });
    try {
      const put = await runCli(putArgs('pr-1'), { env: apiEnv(s) });
      expect(put.code).toBe(0);

      const listed = await runCli(['api', 'deployments', 'list'], { env: apiEnv(s) });
      expect(listed.code).toBe(0);
      const viaCli = JSON.parse(listed.stdout) as { deployments: { id: string }[] };
      const viaHttp = (await (await fetch(`${s.base}/api/deployments`, { headers: s.H })).json()) as {
        deployments: { id: string }[];
      };
      expect(viaCli.deployments.map((d) => d.id)).toEqual(viaHttp.deployments.map((d) => d.id));
      expect(viaCli.deployments.map((d) => d.id)).toContain('pr-1');
    } finally {
      await s.stop();
    }
  }, 30_000);

  test('a path parameter is templated into the URL, not appended as a query', async () => {
    // negative control: send `--id` as `?id=` — the request lands on `/api/deployments`, which
    // exists, returns 200, and would make this pass while `get` was reaching the wrong route. The
    // assertion is therefore on the BODY being one deployment, not on the exit code.
    const s = await bootServer({ tag: 'cli-api-path' });
    try {
      expect((await runCli(putArgs('pr-2'), { env: apiEnv(s) })).code).toBe(0);
      const got = await runCli(['api', 'deployments', 'get', '--id', 'pr-2'], { env: apiEnv(s) });
      expect(got.code).toBe(0);
      const body = JSON.parse(got.stdout) as { id?: string; deployments?: unknown };
      expect(body.id).toBe('pr-2');
      expect(body.deployments).toBeUndefined();
    } finally {
      await s.stop();
    }
  }, 30_000);

  test('a non-2xx is a non-zero exit — a script can branch on it', async () => {
    // negative control: ignore the response status in the executor — this exits 0 on a 404, and
    // every `pstack api … || handle_failure` in a pipeline silently stops firing.
    const s = await bootServer({ tag: 'cli-api-404' });
    try {
      const missing = await runCli(['api', 'deployments', 'get', '--id', 'nope'], { env: apiEnv(s) });
      expect(missing.code).not.toBe(0);
    } finally {
      await s.stop();
    }
  }, 30_000);

  test('the token is attached — without one the same command is refused', async () => {
    // negative control: drop the Auth hook from ExecOptions — the first case below still passes
    // (the server answers) and this one starts passing too, so both are needed to prove the header
    // is actually sent.
    const s = await bootServer({ tag: 'cli-api-auth' });
    try {
      const wrong = await runCli(['api', 'deployments', 'list'], {
        env: { PSTACK_API_URL: s.base, PSTACK_TOKEN: 'not-the-token' },
      });
      expect(wrong.code).not.toBe(0);
      expect(wrong.all).toMatch(/401|unauthorized/i);
    } finally {
      await s.stop();
    }
  }, 30_000);

  test('`--help` works with no host configured, and lists the groups', async () => {
    // negative control: resolve PSTACK_API_URL before building the tree — this prints a sentence
    // about the variable instead of the list, on the first command anyone types.
    const help = await runCli(['api', '--help'], { env: { PSTACK_API_URL: undefined, PSTACK_TOKEN: undefined } });
    expect(help.code).toBe(0);
    for (const group of ['deployments', 'jobs', 'settings', 'notifiers', 'users']) {
      expect(help.stdout).toContain(group);
    }
  }, 20_000);

  test('the top-level usage names `api`, so it is discoverable without reading the docs', async () => {
    // negative control: add the `api` block to the usage text but not to the Usage: line — the
    // verb list a reader scans first would not mention it.
    const top = await runCli(['--help']);
    expect(top.stdout).toContain('|serve|api>');
    expect(top.stdout).toContain('every HTTP route as a command');
  }, 20_000);
});
