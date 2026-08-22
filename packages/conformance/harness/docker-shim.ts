/**
 * A fake `docker` on PATH, answering from a shell `case` over "$*".
 *
 * Arms are written with `printf '%s\n' '…'`, NEVER `echo`: sh's echo mangles the backslashes in a
 * HostRegexp, and the wake-on-call rules are full of them (AGENTS.md says so; it bit once).
 *
 * `recording()` appends every invocation to a log the test can read — the only way to see the argv
 * the API actually issued, since its runners are quiet.
 */
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { tmpd } from './server.ts';

export type Shim = {
  /** Put this first on PATH (`bootServer({ pathPrefix: shim.dir })`). */
  dir: string;
  /** Every "$*" the shim was invoked with, in order. Empty unless `record` was requested. */
  calls: () => string[];
  /** Rewrite the arms between polls — readiness tests change what docker says mid-run. */
  rewrite: (arms: string) => void;
  remove: () => void;
};

export function dockerShim(arms: string, o: { record?: boolean; name?: string } = {}): Shim {
  const dir = tmpd('docker');
  mkdirSync(dir, { recursive: true });
  const log = join(dir, 'calls.log');
  const write = (a: string) =>
    writeFileSync(
      join(dir, o.name ?? 'docker'),
      `#!/bin/sh\n${o.record ? `printf '%s\\n' "$*" >> ${JSON.stringify(log)}\n` : ''}case "$*" in\n${a}\n  *) exit 0 ;;\nesac\n`,
      { mode: 0o755 },
    );
  write(arms);
  return {
    dir,
    calls: () => {
      try {
        return readFileSync(log, 'utf8').split('\n').filter(Boolean);
      } catch {
        return [];
      }
    },
    rewrite: write,
    remove: () => rmSync(dir, { recursive: true, force: true }),
  };
}

/** A docker that says yes to everything — up/down succeed, nothing is listed. */
export const ALWAYS_OK = '';

/** sh-quote a value for use inside an arm (single quotes, the '\'' dance). */
export const sq = (v: string): string => `'${v.split("'").join(`'\\''`)}'`;

/**
 * An arm that prints `text` (with a trailing newline) for a pattern. The pattern is matched against
 * "$*" and double-quoted, so write it exactly as the argv joins (`compose ls --all --format json`);
 * a pattern that needs a glob (`inspect *`) is passed through when it already contains `*`.
 */
export const arm = (pattern: string, text: string, exit = 0): string =>
  `  ${casePattern(pattern)}) printf '%s\\n' ${sq(text)}; exit ${exit} ;;`;

/**
 * A `case` pattern from a human-readable one: every literal run is double-quoted (a space inside
 * an unquoted pattern is a syntax error), every `*` stays a glob. `ps -aq *` → `"ps -aq "*""`.
 */
export const casePattern = (pattern: string): string =>
  // Already written as a shell pattern (quotes present)? Leave it alone.
  pattern.includes('"') ? pattern : pattern.split('*').map((seg) => (seg === '' ? '' : JSON.stringify(seg))).join('*');
