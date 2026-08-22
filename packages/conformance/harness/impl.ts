/**
 * Which implementation this run grades, and how to invoke it.
 *
 *   PSTACK_IMPL=go    the Go binary at $PSTACK_BIN (default packages/pstack/bin/pstack) — the default
 *   PSTACK_IMPL=null  a server answering `200 {}` to everything — the vacuity control. A test that
 *                     passes against it asserts nothing, which `scripts/vacuity.ts` reports.
 *
 * (`bun` graded the TypeScript reference while the port was in flight; the reference is gone and
 * these transcripts are the specification now — docs/port-status.md.)
 *
 * Nothing here imports the implementation. The CLI is an argv, the server is a process, and the
 * harness only ever sees a port.
 */
import { existsSync } from 'node:fs';
import { dirname, join } from 'node:path';

export type Impl = 'go' | 'null';

const raw = process.env.PSTACK_IMPL ?? 'go';
if (raw !== 'go' && raw !== 'null') {
  throw new Error(`PSTACK_IMPL must be go or null — got "${raw}"`);
}
export const IMPL: Impl = raw;

/** The monorepo root: walk up from this file until turbo.json. */
export const REPO = ((): string => {
  let dir = dirname(new URL(import.meta.url).pathname);
  for (let i = 0; i < 8; i++) {
    if (existsSync(join(dir, 'turbo.json'))) return dir;
    dir = dirname(dir);
  }
  throw new Error('could not find the repo root (no turbo.json above harness/)');
})();

/** Where CLI tests run from, so `examples/…` and relative spec paths resolve the same in every mode. */
export const CLI_CWD = join(REPO, 'packages', 'pstack');

export const GO_BIN = process.env.PSTACK_BIN ?? join(REPO, 'packages', 'pstack', 'bin', 'pstack');

/** The argv that runs `pstack <args>` for the selected implementation. */
export function cliArgv(args: string[]): string[] {
  switch (IMPL) {
    case 'go':
      if (!existsSync(GO_BIN)) {
        throw new Error(`PSTACK_IMPL=go but no binary at ${GO_BIN} — build it (bun run build in packages/pstack) or set PSTACK_BIN`);
      }
      return [GO_BIN, ...args];
    case 'null':
      throw new Error('PSTACK_IMPL=null has no CLI — only a server. CLI tests are skipped in this mode.');
  }
}

/** `describe.skipIf(NO_CLI)` for CLI-only suites. */
export const NO_CLI = IMPL === 'null';

/**
 * The inherited environment minus anything that would make a run depend on the developer's shell:
 * every PSTACK_* variable, DOCKER_CONFIG, and the example's own PR/GIT_SHA. A case that needs one
 * sets it explicitly.
 */
export function cleanEnv(): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (v === undefined) continue;
    if (k.startsWith('PSTACK_') || k === 'DOCKER_CONFIG' || k === 'PR' || k === 'GIT_SHA') continue;
    out[k] = v;
  }
  return out;
}
