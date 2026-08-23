/**
 * The CLI transcripts are the contract.
 *
 * Every case in gen/goldens.table.ts has its exit code, stdout and stderr checked in under
 * golden/cli/ (generated once from the TypeScript reference the binary was ported against, and
 * byte-identical since). This test replays the same table against the binary and compares after
 * masking what legitimately varies (the data dir, the version, a generated token).
 *
 * The rendered control directory (docker-compose.yml, .env, dns.env for all eight cells) is
 * compared the same way: it is what `upgrade` reads back, and what keeps the letsencrypt volume
 * named the same across versions.
 */
import { describe, expect, test } from 'bun:test';
import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { NO_CLI } from '../harness/impl.ts';
import { runCli } from '../harness/cli.ts';
import { dockerShim } from '../harness/docker-shim.ts';
import { CASES, DATA_DIR } from '../gen/goldens.table.ts';
import { GOLDEN, mask, type CliGolden } from '../harness/goldens.ts';

describe.skipIf(NO_CLI)('CLI goldens', () => {
  /**
   * Captured on FIRST USE, not in the test below.
   *
   * `mask()` replaces it with <VERSION>, so every case needs it — including a single case run
   * under `-t <name>`, which filters the test below out. Holding it only there left `version` as
   * the empty string, and `''.split()` shreds the transcript into characters: a filtered run
   * diffed every character of the output against itself. CI runs exactly such a filtered case.
   */
  let version = '';
  const implVersion = async (): Promise<string> => {
    if (!version) version = (await runCli(['--version'])).stdout.trim();
    return version;
  };
  test('the implementation reports a version', async () => {
    expect(await implVersion()).toMatch(/^\d+\.\d+\.\d+/);
  });

  // Cases run IN TABLE ORDER in one data dir — an upgrade plan reads what the init before it wrote.
  for (const c of CASES) {
    test(c.name, async () => {
      const golden = JSON.parse(readFileSync(join(GOLDEN, 'cli', `${c.name}.json`), 'utf8')) as CliGolden;
      const version = await implVersion();
      if (c.freshData) {
        rmSync(DATA_DIR, { recursive: true, force: true });
        mkdirSync(DATA_DIR, { recursive: true });
      }
      const shim = c.shim !== undefined ? dockerShim(c.shim) : null;
      try {
        const r = await runCli(c.argv, { env: c.env, pathPrefix: shim?.dir });
        expect(r.code).toBe(golden.code);
        expect(mask(r.stdout, version)).toBe(golden.stdout);
        expect(mask(r.stderr, version)).toBe(golden.stderr);

        if (c.render) {
          for (const f of c.render.files) {
            const goldenPath = join(GOLDEN, 'render', c.render.dir, f.replace(/^control\//, ''));
            const livePath = join(DATA_DIR, f);
            // Both absent (an http01 host still writes dns.env, but be honest either way) or both present and equal.
            expect(existsSync(livePath)).toBe(existsSync(goldenPath));
            if (existsSync(goldenPath)) {
              expect(mask(readFileSync(livePath, 'utf8'), version)).toBe(readFileSync(goldenPath, 'utf8'));
            }
          }
        }
      } finally {
        shim?.remove();
      }
    }, 20_000);
  }
});
