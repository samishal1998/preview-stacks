/**
 * Produce golden/cli/*.json and golden/render/** from the Bun reference.
 *
 *   bun gen/goldens.ts            (forces PSTACK_IMPL=bun; run only while packages/pstack/src exists)
 *
 * One case = one CLI run with a pinned environment; init cases also copy the control directory
 * they rendered. The consumer (test/cli-goldens.test.ts) replays the same table against whichever
 * implementation is selected and compares after masking.
 */
if (process.env.PSTACK_IMPL && process.env.PSTACK_IMPL !== 'bun') {
  throw new Error('goldens are generated from the Bun reference only — unset PSTACK_IMPL');
}
process.env.PSTACK_IMPL = 'bun';

const { existsSync, mkdirSync, readdirSync, rmSync, writeFileSync } = await import('node:fs');
const { dirname, join } = await import('node:path');
const { runCli } = await import('../harness/cli.ts');
const { dockerShim } = await import('../harness/docker-shim.ts');
const { CASES, DATA_DIR } = await import('./goldens.table.ts');
const { GOLDEN, mask } = await import('../harness/goldens.ts');
const { CLI_CWD } = await import('../harness/impl.ts');

const version = (await runCli(['--version'])).stdout.trim();
console.log(`reference: pstack ${version}`);
// The reference's files only: `<name>.go.json` (gen/goldens-go.ts) are the Go build's and stay.
mkdirSync(join(GOLDEN, 'cli'), { recursive: true });
for (const f of readdirSync(join(GOLDEN, 'cli'))) {
  if (f.endsWith('.json') && !f.endsWith('.go.json')) rmSync(join(GOLDEN, 'cli', f));
}
rmSync(join(GOLDEN, 'render', 'control'), { recursive: true, force: true });

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
      ...(c.goDivergent ? { goDivergent: c.goDivergent.source } : {}),
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
