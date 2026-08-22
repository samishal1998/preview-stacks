/**
 * Produce golden/cli/<name>.go.json for the cases whose Go transcript is its own document (the
 * control Dockerfile, the cloud-init runcmd) — everything a `goGolden: true` case in the table names.
 *
 *   bun gen/goldens-go.ts         (forces PSTACK_IMPL=go; needs packages/pstack/bin/pstack built)
 *
 * These are checked in and, once the TS reference is gone, are the spec for those commands. The
 * reference's own `<name>.json` stays beside them as the TS contract.
 */
if (process.env.PSTACK_IMPL && process.env.PSTACK_IMPL !== 'go') {
  throw new Error('Go goldens are generated from the Go binary only — set PSTACK_IMPL=go or unset it');
}
process.env.PSTACK_IMPL = 'go';

const { writeFileSync } = await import('node:fs');
const { join } = await import('node:path');
const { runCli } = await import('../harness/cli.ts');
const { dockerShim } = await import('../harness/docker-shim.ts');
const { CASES } = await import('./goldens.table.ts');
const { GOLDEN, mask } = await import('../harness/goldens.ts');
const { CLI_CWD } = await import('../harness/impl.ts');

const version = (await runCli(['--version'])).stdout.trim();
console.log(`go: pstack ${version}`);
for (const c of CASES) {
  if (!c.goGolden) continue;
  const shim = c.shim !== undefined ? dockerShim(c.shim) : null;
  try {
    const r = await runCli(c.argv, { env: c.env, pathPrefix: shim?.dir });
    const golden = { name: c.name, argv: c.argv, env: c.env ?? {}, cwd: CLI_CWD, code: r.code, stdout: mask(r.stdout, version), stderr: mask(r.stderr, version) };
    writeFileSync(join(GOLDEN, 'cli', `${c.name}.go.json`), JSON.stringify(golden, null, 2) + '\n');
    console.log(`  ${c.name}.go.json  (exit ${r.code})`);
  } finally {
    shim?.remove();
  }
}
export {};
