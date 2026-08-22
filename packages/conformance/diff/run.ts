/**
 * Run every scenario against binary A then binary B, on the same data path, and compare.
 *
 *   bun diff/run.ts --self                          A = B = the built binary  (must be identical:
 *                                                   the mask list's own control)
 *   bun diff/run.ts --a /usr/local/bin/pstack       a released binary vs the working tree — the
 *                                                   regression diff before a release
 *   bun diff/run.ts --a <bin> --b <bin> --only notifiers
 *
 * This is the tool that graded the Go port against the TypeScript reference step for step until
 * the two were byte-identical (docs/port-status.md). On the first differing step of a scenario,
 * both records are printed and both full traces are written to .diff/<scenario>.{a,b}.json.
 */
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { KNOWN_DEVIATIONS, compare, type Step } from '../harness/diff.ts';
import { GO_BIN } from '../harness/impl.ts';
import { SCENARIOS } from './scenarios/index.ts';

const argv = process.argv.slice(2);
const flag = (k: string) => (argv.includes(k) ? argv[argv.indexOf(k) + 1] : undefined);
const self = argv.includes('--self');
const A = self ? GO_BIN : (flag('--a') ?? GO_BIN);
const B = self ? GO_BIN : (flag('--b') ?? GO_BIN);
const only = flag('--only');
const PKG = new URL('..', import.meta.url).pathname;
const OUT = join(PKG, '.diff');
mkdirSync(OUT, { recursive: true });

async function runOn(bin: string, name: string, dataDir: string): Promise<Step[]> {
  // A fresh process with the binary pinned — the harness reads it at import time.
  const proc = Bun.spawn(['bun', join(PKG, 'diff', 'one.ts'), name, dataDir], {
    env: { ...process.env, PSTACK_IMPL: 'go', PSTACK_BIN: bin },
    stdout: 'pipe',
    stderr: 'pipe',
  });
  const [out, err, code] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
  if (code !== 0) throw new Error(`scenario ${name} on ${bin} crashed:\n${err.slice(-3000)}`);
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

  const skip = self ? new Set<number>() : new Set(KNOWN_DEVIATIONS.filter((k) => k.scenario === sc.name).map((k) => k.step));
  const d = compare(a, b, skip);
  for (const i of d?.skipped ?? []) {
    seenDeviations.add(`${sc.name}:${i}`);
    const known = KNOWN_DEVIATIONS.find((k) => k.scenario === sc.name && k.step === i)!;
    console.log(`  ~ ${sc.name}  step ${i} differs as documented: ${known.why}`);
  }
  if (!d || d.index < 0) {
    console.log(`  ✓ ${sc.name}  (${a.length} steps)`);
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
