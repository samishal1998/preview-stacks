/**
 * THE mechanical negative control for the whole suite.
 *
 * Runs every test against PSTACK_IMPL=null — a server that answers `200 {}` to everything — and
 * reports every test that PASSED. Such a test asserts nothing about pstack; it would pass against
 * any implementation, including a wrong one. Exit 1 if any exist.
 *
 * CLI-only tests are skipped in null mode by design (describe.skipIf(NO_CLI)); a skip is not a pass.
 */
import { runJunit } from './junit.ts';

const { cases } = await runJunit('null');
const vacuous = cases.filter((c) => c.verdict === 'pass');
const skipped = cases.filter((c) => c.verdict === 'skip').length;
const failed = cases.filter((c) => c.verdict === 'fail').length;
console.log(`null mode: ${failed} failed (good), ${skipped} skipped (CLI-only), ${vacuous.length} vacuous`);
if (vacuous.length > 0) {
  console.error('\nThese tests PASS against a server that answers 200 {} to everything — they assert nothing:');
  for (const c of vacuous) console.error(`  ${c.file}  ${c.classname} > ${c.name}`);
  process.exit(1);
}
