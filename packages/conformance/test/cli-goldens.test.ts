/**
 * The CLI transcripts are the contract.
 *
 * Every case in gen/goldens.table.ts was run once against the Bun reference and its exit code,
 * stdout and stderr checked in under golden/cli/. This test replays the same table against the
 * selected implementation and compares after masking what legitimately varies (the data dir, the
 * version, Bun's version, a generated token). In go mode the lines that diverge BY DESIGN (the Bun
 * install block in cloud-init, the npm-based Dockerfiles and upgrade step) are dropped from both
 * sides first; everything else must be byte-identical.
 *
 * The rendered control directory (docker-compose.yml, .env, dns.env for all eight cells) is
 * compared the same way: it is what `upgrade` reads back, and what keeps the letsencrypt volume
 * named the same across the port.
 */
import { describe, expect, test } from 'bun:test';
import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { IMPL, NO_CLI } from '../harness/impl.ts';
import { runCli } from '../harness/cli.ts';
import { dockerShim } from '../harness/docker-shim.ts';
import { CASES, DATA_DIR } from '../gen/goldens.table.ts';
import { GOLDEN, mask, withoutDivergent, type CliGolden } from '../harness/goldens.ts';

describe.skipIf(NO_CLI)('CLI goldens', () => {
  let version = '';
  test('the implementation reports a version', async () => {
    version = (await runCli(['--version'])).stdout.trim();
    expect(version).toMatch(/^\d+\.\d+\.\d+/);
  });

  // Cases run IN TABLE ORDER in one data dir — an upgrade plan reads what the init before it wrote.
  for (const c of CASES) {
    test.skipIf(IMPL === 'go' && !!c.bunOnly)(c.name, async () => {
      // A case whose Go transcript is its own document reads `<name>.go.json` in go mode (gen/goldens-go.ts).
      const file = IMPL === 'go' && c.goGolden ? `${c.name}.go.json` : `${c.name}.json`;
      const golden = JSON.parse(readFileSync(join(GOLDEN, 'cli', file), 'utf8')) as CliGolden;
      if (c.freshData) {
        rmSync(DATA_DIR, { recursive: true, force: true });
        mkdirSync(DATA_DIR, { recursive: true });
      }
      const shim = c.shim !== undefined ? dockerShim(c.shim) : null;
      try {
        const r = await runCli(c.argv, { env: c.env, pathPrefix: shim?.dir });
        // The table's CURRENT expression, not the one stamped into the file when it was generated.
        const divergent = IMPL === 'go' && c.goDivergent ? c.goDivergent : undefined;
        expect(r.code).toBe(golden.code);
        expect(withoutDivergent(mask(r.stdout, version), divergent)).toBe(withoutDivergent(golden.stdout, divergent));
        expect(withoutDivergent(mask(r.stderr, version), divergent)).toBe(withoutDivergent(golden.stderr, divergent));

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
