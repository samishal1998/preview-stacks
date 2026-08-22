/**
 * Produce golden/cli/*.json and golden/render/** from the built binary.
 *
 *   bun gen/goldens.ts            (needs packages/pstack/bin/pstack; PSTACK_BIN overrides)
 *
 * THE GOLDENS ARE THE SPECIFICATION. They were generated once from the TypeScript reference the
 * Go binary was ported against and have been byte-identical since (docs/port-status.md); a change
 * to them is a deliberate contract change and is reviewed as one. Run this after such a change and
 * commit the diff with the code.
 *
 * One case = one CLI run with a pinned environment; init cases also copy the control directory
 * they rendered. The consumer (test/cli-goldens.test.ts) replays the same table and compares
 * after masking.
 */
if (process.env.PSTACK_IMPL && process.env.PSTACK_IMPL !== 'go') {
  throw new Error('goldens are generated from the Go binary — unset PSTACK_IMPL');
}
process.env.PSTACK_IMPL = 'go';

const { existsSync, mkdirSync, rmSync, writeFileSync } = await import('node:fs');
const { dirname, join } = await import('node:path');
const { runCli } = await import('../harness/cli.ts');
const { dockerShim } = await import('../harness/docker-shim.ts');
const { CASES, DATA_DIR } = await import('./goldens.table.ts');
const { GOLDEN, mask } = await import('../harness/goldens.ts');
const { CLI_CWD } = await import('../harness/impl.ts');

const version = (await runCli(['--version'])).stdout.trim();
console.log(`pstack ${version}`);
rmSync(join(GOLDEN, 'cli'), { recursive: true, force: true });
rmSync(join(GOLDEN, 'render', 'control'), { recursive: true, force: true });
mkdirSync(join(GOLDEN, 'cli'), { recursive: true });

for (const c of CASES) {
  if (c.freshData) {
    rmSync(DATA_DIR, { recursive: true, force: true });
    mkdirSync(DATA_DIR, { recursive: true });
  }
  const shim = c.shim !== undefined ? dockerShim(c.shim) : null;
  try {
    const r = await runCli(c.argv, { env: c.env, pathPrefix: shim?.dir });
    const golden = {
      name: c.name,
      argv: c.argv,
      env: c.env ?? {},
      cwd: 'packages/pstack',
      code: r.code,
      stdout: mask(r.stdout, version),
      stderr: mask(r.stderr, version),
    };
    writeFileSync(join(GOLDEN, 'cli', `${c.name}.json`), JSON.stringify(golden, null, 2) + '\n');
    if (c.render) {
      for (const f of c.render.files) {
        const src = join(DATA_DIR, f);
        if (!existsSync(src)) continue;
        const dst = join(GOLDEN, 'render', c.render.dir, f.replace(/^control\//, ''));
        mkdirSync(dirname(dst), { recursive: true });
        const text = await Bun.file(src).text();
        writeFileSync(dst, mask(text, version));
      }
    }
    console.log(`  ${c.name}: exit ${r.code}`);
  } finally {
    shim?.remove();
  }
}
void CLI_CWD;
console.log(`wrote ${CASES.length} goldens`);

export {};
