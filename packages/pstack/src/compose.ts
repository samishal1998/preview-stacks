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
import { subdomainEnv } from './subdomains.ts';
import { materializeCompose } from './autolabel.ts';

function fileArgs(spec: Stack): string {
  const c = spec.compose!;
  return [c.file, ...(c.overlays ?? [])].map((f) => `-f ${shq(f)}`).join(' ');
}

/**
 * The prefix every subcommand shares.
 *
 * `-p <stack>` is the namespacing primitive: it prefixes containers, networks and volumes, so two
 * stacks from the same compose file never collide. The `-f` list comes from `fileArgsFor`, which
 * substitutes the augmented file when the submitted one asked for generated labels.
 */
async function baseFor(spec: Stack, runner: Runner): Promise<string> {
  return `docker compose -p ${shq(spec.stack)} ${await fileArgsFor(spec, runner)}`;
}

/**
 * Resolve which compose file to pass to `-f`, generating the augmented one when the submitted file
 * asks for it with `pstack.routing.*` labels.
 *
 * Called by EVERY subcommand, not just `up`. The generated labels are derived from the resolved spec,
 * so regenerating each time is what stops `up` and `down` disagreeing about what a router was called —
 * and compose reads the file on every subcommand anyway.
 *
 * `runner.cwd` is the deployment directory (the registry sets it) or the spec's own directory (the
 * CLI). With neither there is nowhere to write, so the submitted file is used unchanged.
 */
async function fileArgsFor(spec: Stack, runner: Runner): Promise<string> {
  const c = spec.compose!;
  const dir = runner.cwd;
  // Nothing is written under --dry-run: a dry run must not have side effects, and the point of it is
  // to show what WOULD happen.
  if (!dir || runner.dryRun) return fileArgs(spec);
  const { file } = await materializeCompose({ dir, spec, runner });
  return [file, ...(c.overlays ?? [])].map((f) => `-f ${shq(f)}`).join(' ');
}

function profileArgs(profiles: string[]): string {
  return profiles.map((p) => `--profile ${shq(p)}`).join(' ');
}

/**
 * The environment every compose subcommand runs with.
 *
 * The wildcard-subdomain rules go in here rather than only into `up` because compose interpolates the
 * compose FILE on every subcommand. A label that reads `${PSTACK_WILD_BACKEND}` would otherwise make
 * `down`, `logs` and `ps` warn about an unset variable and substitute an empty string — and on `down`
 * that means compose is reasoning about a *differently-labelled* project than the one `up` created.
 */
function composeEnv(spec: Stack, extra: Record<string, string> = {}): Record<string, string> {
  return {
    ...spec.env,
    ...subdomainEnv(spec.compose?.subdomains ?? []),
    ...extra,
    STACK: spec.stack,
  };
}

export async function composeUp(
  spec: Stack,
  runner: Runner,
  extraEnv: Record<string, string>,
): Promise<RunResult> {
  const c = spec.compose!;
  const cmd = `${await baseFor(spec, runner)} ${profileArgs(c.profiles)} up -d --remove-orphans`;
  // --remove-orphans drops services that were in a previous deploy but are not selected now, so a
  // relabel from "backend+frontend" to "backend" actually stops the frontend instead of orphaning it.
  return runner.run(cmd, { env: composeEnv(spec, extraEnv), label: 'compose up' });
}

export async function composeDown(spec: Stack, runner: Runner): Promise<RunResult> {
  const c = spec.compose!;
  // EVERY profile — see the file header.
  const cmd = `${await baseFor(spec, runner)} ${profileArgs(c.profiles)} down -v --remove-orphans`;
  return runner.run(cmd, { env: composeEnv(spec), label: 'compose down' });
}

/**
 * The `logs` command line and the environment it needs, without running it.
 *
 * Split out because FOLLOWING logs cannot go through `Runner`: that buffers a process to completion,
 * and `--follow` never completes. The SSE route spawns this itself and streams the pipe. Both paths
 * building their command here is what stops the followed stream and the fetched one disagreeing
 * about which project, profiles or files they are reading.
 */
export async function composeLogsCommand(
  spec: Stack,
  runner: Runner,
  tail: number,
  service?: string,
  opts: { timestamps?: boolean; since?: string; until?: string; follow?: boolean } = {},
): Promise<{ cmd: string; env: Record<string, string> } | null> {
  const c = spec.compose;
  if (!c) return null;
  const only = service ? ` ${shq(service)}` : '';
  const flags = [
    opts.follow ? ' --follow' : '',
    opts.timestamps ? ' --timestamps' : '',
    opts.since ? ` --since ${shq(opts.since)}` : '',
    opts.until ? ` --until ${shq(opts.until)}` : '',
  ].join('');
  return {
    cmd: `${await baseFor(spec, runner)} ${profileArgs(c.profiles)} logs --no-color --tail ${Math.trunc(tail)}${flags}${only}`,
    env: composeEnv(spec),
  };
}

/**
 * Recent logs for a stack. `--no-color` because the output is rendered in a browser, where ANSI
 * escapes are noise, and `--tail` because an unbounded fetch on a chatty stack would stream
 * megabytes into a tab. Every profile is enabled for the same reason `down` enables them: compose
 * treats an unenabled profile's services as absent, so their logs would silently be missing.
 */
export async function composeLogs(
  spec: Stack,
  runner: Runner,
  tail: number,
  /**
   * One compose SERVICE to read, instead of the whole stack. On a stack with a chatty sidecar the
   * interleaved output is unreadable and the interesting lines are already past the tail, so narrowing
   * is often the difference between finding the error and not.
   *
   * Shell-quoted, because it arrives from a query parameter and is interpolated into a command.
   */
  service?: string,
  /**
   * The rest of what `docker compose logs` can tell you, which this used to throw away.
   *
   * `timestamps` is the one that matters most: without it a log line cannot be lined up against a
   * deploy, a healthcheck flap or another service's line, and "when did this start" is unanswerable
   * from the page. `since`/`until` are what make a long-lived container readable at all — a tail of
   * 2000 on a service that logs every request is 2000 lines of the last four minutes.
   */
  opts: { timestamps?: boolean; since?: string; until?: string } = {},
): Promise<RunResult> {
  // `logs [SERVICE...]` — an unknown name makes compose exit non-zero with its own message, which is
  // better than anything this layer could invent.
  const built = await composeLogsCommand(spec, runner, tail, service, opts);
  if (!built) return { ok: true, code: 0, stdout: '', stderr: '(no compose section in spec)', skipped: false };
  return runner.run(built.cmd, {
    env: built.env,
    label: service ? `compose logs ${service}` : 'compose logs',
  });
}

export async function composePs(spec: Stack, runner: Runner): Promise<RunResult> {
  const c = spec.compose!;
  return runner.run(`${await baseFor(spec, runner)} ${profileArgs(c.profiles)} ps`, {
    env: composeEnv(spec),
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
