#!/usr/bin/env bun
/**
 * Build the publishable artifact.
 *
 * `pstack` installs globally, so what ships is a BUNDLE, never the source tree. Shipping `src/`
 * would mean every user pays TypeScript parsing on each invocation, downloads docs/examples/skills
 * they did not ask for, and gets a package whose internals are its public surface.
 *
 * Two outputs:
 *
 *   dist/cli.js    the CLI + API + UI, one file. `bin` points here.
 *   dist/index.js  the library entry, for embedding the lifecycle in your own tooling.
 *
 * Both are `--target=bun`, not `--compile`: a compiled standalone executable bakes in the Bun
 * runtime (~60 MB) *per platform*, which for an npm package means either a 5-platform matrix of
 * optionalDependencies or a download hook. A bundle is ~100 KB and Bun is already present — the
 * package declares `engines.bun`, and there is no Node fallback to preserve because the runtime
 * APIs used (Bun.serve, Bun.YAML, Bun.spawn, Bun.file) have no Node equivalent.
 *
 * The UI and the control-stack compose template are `with { type: 'text' }` imports, so they are
 * inlined into the bundle. That is deliberate: nothing is read from a path relative to the source
 * at runtime, which is the failure mode where a package works from a checkout and 404s once
 * installed.
 */

import { rm } from 'node:fs/promises';

const OUT = 'dist';

await rm(OUT, { recursive: true, force: true });

const result = await Bun.build({
  entrypoints: ['src/cli.ts', 'src/index.ts'],
  outdir: OUT,
  target: 'bun',
  minify: true,
  sourcemap: 'linked', // a stack trace from a user's box should still name real functions
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

// The bin must be executable and self-locating. Bun's bundler does not emit a shebang, and npm only
// creates the PATH shim — without this, `pstack` fails with "cannot execute binary file".
const cliPath = `${OUT}/cli.js`;
const code = await Bun.file(cliPath).text();
if (!code.startsWith('#!')) {
  await Bun.write(cliPath, `#!/usr/bin/env bun\n${code}`);
}
await Bun.spawn(['chmod', '+x', cliPath]).exited;

let total = 0;
for (const out of result.outputs) {
  const kb = (out.size / 1024).toFixed(1);
  total += out.size;
  console.log(`  ${out.path.replace(process.cwd() + '/', '')}  ${kb} KB`);
}
console.log(`\nbundled ${result.outputs.length} file(s), ${(total / 1024).toFixed(1)} KB total`);

// A bundle that cannot start is not a build. Smoke-test the real artifact, not the sources.
const proc = Bun.spawn(['bun', cliPath, '--help'], { stdout: 'pipe', stderr: 'pipe' });
const [out, exited] = await Promise.all([new Response(proc.stdout).text(), proc.exited]);
if (exited !== 0 || !out.includes('pstack')) {
  console.error(`\nbuilt bundle failed to run (exit ${exited}):\n${out}`);
  process.exit(1);
}
console.log('smoke: `pstack --help` runs from the bundle');
