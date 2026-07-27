/**
 * Docker Compose invocation.
 *
 * The one non-obvious rule, and the reason this file exists instead of an inline string:
 *
 *   `up` passes only the SELECTED profiles. `down` passes EVERY profile in the spec.
 *
 * Compose treats a service whose profile is not enabled as absent. So a stack brought up with
 * `--profile backend` and torn down with the same flag leaves the *other* profiles' resources
 * behind — most visibly the project's default network, which then accumulates one dead
 * `<stack>_default` per PR forever. Enumerating every profile on `down` costs nothing (passing a
 * profile with no matching service is a no-op) and is the difference between a clean box and a
 * slow leak.
 *
 * Second rule: `down -v` removes containers, volumes and networks — but NOT images. Per-stack
 * images are removed by an explicit axis (see examples/), not by compose.
 */

import type { Stack } from './spec.ts';
import type { Runner, RunResult } from './exec.ts';

function fileArgs(spec: Stack): string {
  const c = spec.compose!;
  return [c.file, ...(c.overlays ?? [])].map((f) => `-f ${shq(f)}`).join(' ');
}

function base(spec: Stack): string {
  // `-p <stack>` is the namespacing primitive: it prefixes containers, networks and volumes, so two
  // stacks from the same compose file never collide.
  return `docker compose -p ${shq(spec.stack)} ${fileArgs(spec)}`;
}

function profileArgs(profiles: string[]): string {
  return profiles.map((p) => `--profile ${shq(p)}`).join(' ');
}

export async function composeUp(
  spec: Stack,
  runner: Runner,
  extraEnv: Record<string, string>,
): Promise<RunResult> {
  const c = spec.compose!;
  const cmd =
    `${base(spec)} ${profileArgs(c.profiles)} up -d --remove-orphans`;
  // --remove-orphans drops services that were in a previous deploy but are not selected now, so a
  // relabel from "backend+frontend" to "backend" actually stops the frontend instead of orphaning it.
  return runner.run(cmd, {
    env: { ...spec.env, ...extraEnv, STACK: spec.stack },
    label: 'compose up',
  });
}

export async function composeDown(spec: Stack, runner: Runner): Promise<RunResult> {
  const c = spec.compose!;
  // EVERY profile — see the file header.
  const cmd = `${base(spec)} ${profileArgs(c.profiles)} down -v --remove-orphans`;
  return runner.run(cmd, { env: { ...spec.env, STACK: spec.stack }, label: 'compose down' });
}

/**
 * Recent logs for a stack. `--no-color` because the output is rendered in a browser, where ANSI
 * escapes are noise, and `--tail` because an unbounded fetch on a chatty stack would stream
 * megabytes into a tab. Every profile is enabled for the same reason `down` enables them: compose
 * treats an unenabled profile's services as absent, so their logs would silently be missing.
 */
export async function composeLogs(spec: Stack, runner: Runner, tail: number): Promise<RunResult> {
  const c = spec.compose;
  if (!c) return { ok: true, code: 0, stdout: '', stderr: '(no compose section in spec)', skipped: false };
  return runner.run(
    `${base(spec)} ${profileArgs(c.profiles)} logs --no-color --tail ${Math.trunc(tail)}`,
    { env: { ...spec.env, STACK: spec.stack }, label: 'compose logs' },
  );
}

export async function composePs(spec: Stack, runner: Runner): Promise<RunResult> {
  const c = spec.compose!;
  return runner.run(`${base(spec)} ${profileArgs(c.profiles)} ps`, {
    env: { ...spec.env, STACK: spec.stack },
    label: 'compose ps',
  });
}

/**
 * Single-quote a value for bash. Wrapping in single quotes and escaping embedded single quotes is
 * the only form that is safe for arbitrary content — a stack name or path with a space, a quote or
 * a `$` would otherwise be re-split or expanded by the shell.
 */
export function shq(v: string): string {
  return `'${v.split("'").join(`'\\''`)}'`;
}
