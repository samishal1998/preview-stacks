/**
 * Run every scenario against implementation A then B, on the same data path, and compare.
 *
 *   bun diff/run.ts                 A=bun  B=go
 *   bun diff/run.ts --self          A=bun  B=bun   (must be identical: the mask list's own control)
 *   bun diff/run.ts --a bun --b go --only notifiers
 *
 * On the first differing step of a scenario, both records are printed and both full traces are
 * written to .diff/<scenario>.{a,b}.json for `diff -u`.
 */
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { KNOWN_DEVIATIONS, compare, type Step } from '../harness/diff.ts';
import { SCENARIOS } from './scenarios/index.ts';

const argv = process.argv.slice(2);
const flag = (k: string) => (argv.includes(k) ? argv[argv.indexOf(k) + 1] : undefined);
const self = argv.includes('--self');
const A = flag('--a') ?? 'bun';
const B = self ? 'bun' : (flag('--b') ?? 'go');
const only = flag('--only');
const PKG = new URL('..', import.meta.url).pathname;
const OUT = join(PKG, '.diff');
mkdirSync(OUT, { recursive: true });

async function runOn(impl: string, name: string, dataDir: string): Promise<Step[]> {
  // A fresh process with PSTACK_IMPL pinned — the harness reads it at import time.
  const proc = Bun.spawn(['bun', join(PKG, 'diff', 'one.ts'), name, dataDir], {
    env: { ...process.env, PSTACK_IMPL: impl },
    stdout: 'pipe',
    stderr: 'pipe',
  });
  const [out, err, code] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
  if (code !== 0) throw new Error(`scenario ${name} on ${impl} crashed:\n${err.slice(-3000)}`);
  return JSON.parse(out) as Step[];
}

let failures = 0;
const seenDeviations = new Set<string>();
for (const sc of SCENARIOS) {
  if (only && sc.name !== only) continue;
  const dataDir = `${(process.env.TMPDIR ?? '/tmp').replace(/\/$/, '')}/pstack-diff-${sc.name}-${process.pid}`;
  rmSync(dataDir, { recursive: true, force: true });
  const a = await runOn(A, sc.name, dataDir);
  rmSync(dataDir, { recursive: true, force: true });
  const b = await runOn(B, sc.name, dataDir);
  rmSync(dataDir, { recursive: true, force: true });
  writeFileSync(join(OUT, `${sc.name}.a.json`), JSON.stringify(a, null, 2));
  writeFileSync(join(OUT, `${sc.name}.b.json`), JSON.stringify(b, null, 2));

  const d = compare(a, b);
  const known = d ? KNOWN_DEVIATIONS.find((k) => k.scenario === sc.name && k.step === d.index) : undefined;
  if (!d) {
    console.log(`  ✓ ${sc.name}  (${a.length} steps)`);
  } else if (known && !self) {
    seenDeviations.add(`${sc.name}:${d.index}`);
    console.log(`  ~ ${sc.name}  step ${d.index} differs as documented: ${known.why}`);
  } else {
    failures++;
    console.log(`  ✗ ${sc.name}  first difference at step ${d.index} (${d.a?.method ?? d.b?.method} ${d.a?.path ?? d.b?.path})`);
    console.log(`    ${A}: ${JSON.stringify(d.a).slice(0, 600)}`);
    console.log(`    ${B}: ${JSON.stringify(d.b).slice(0, 600)}`);
  }
}
for (const k of KNOWN_DEVIATIONS) {
  if (only && k.scenario !== only) continue;
  if (!self && !seenDeviations.has(`${k.scenario}:${k.step}`)) {
    failures++;
    console.log(`  ! KNOWN_DEVIATIONS lists ${k.scenario} step ${k.step} but it no longer differs — remove the entry`);
  }
}
console.log(failures ? `\n${failures} scenario(s) differ — traces in .diff/` : `\nall scenarios identical (${A} vs ${B})`);
process.exit(failures ? 1 : 0);
