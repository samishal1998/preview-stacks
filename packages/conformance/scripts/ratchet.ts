/**
 * Go-mode progress only goes UP.
 *
 * expected-pass.json records, per test file, how many tests the Go binary passes. This script runs
 * the suite in go mode and fails if any file's count fell; it prints the files whose count rose so
 * the PR bumps the number. A port that regresses a route group cannot merge; a port that completes
 * one records the fact.
 *
 *   bun scripts/ratchet.ts            check
 *   bun scripts/ratchet.ts --write    record the current counts (after a deliberate step forward)
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { PKG, runJunit } from './junit.ts';

const file = join(PKG, 'expected-pass.json');
const expected = JSON.parse(readFileSync(file, 'utf8')) as { go: Record<string, number> };
const { cases } = await runJunit('go');
const actual: Record<string, number> = {};
for (const c of cases) actual[c.file] = (actual[c.file] ?? 0) + (c.verdict === 'pass' ? 1 : 0);
const total: Record<string, number> = {};
for (const c of cases) total[c.file] = (total[c.file] ?? 0) + (c.verdict === 'skip' ? 0 : 1);

let regressed = 0;
let advanced = 0;
for (const f of Object.keys({ ...expected.go, ...actual }).sort()) {
  const want = expected.go[f] ?? 0;
  const got = actual[f] ?? 0;
  const mark = got < want ? 'REGRESSED' : got > want ? 'advanced' : got === (total[f] ?? 0) && got > 0 ? 'complete' : '';
  console.log(`  ${f.padEnd(48)} ${String(got).padStart(3)}/${String(total[f] ?? 0).padEnd(3)} (recorded ${want}) ${mark}`);
  if (got < want) regressed++;
  if (got > want) advanced++;
}
if (process.argv.includes('--write')) {
  writeFileSync(file, JSON.stringify({ go: Object.fromEntries(Object.entries(actual).sort()) }, null, 2) + '\n');
  console.log('recorded.');
} else if (regressed) {
  console.error(`\n${regressed} file(s) regressed in go mode.`);
  process.exit(1);
} else if (advanced) {
  console.log(`\n${advanced} file(s) pass more than recorded — run with --write to ratchet up.`);
}
