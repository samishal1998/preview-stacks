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
 *
 * Third, since 0.26.0: every function here DISPATCHES on `spec.compose.orchestrator`. Under `swarm`
 * the same verbs become `docker stack deploy` / `docker stack rm` / `docker service logs` /
 * `docker stack ps` (built in swarm.ts), and the file passed is the converted one. The callers in
 * stack.ts and api.ts do not know which; that is the point of keeping one seam.
 *
 * `sleep` is the fourth verb: `down` WITHOUT `-v` (swarm: `stack rm`, which never touches volumes).
 * The containers and networks go, the data stays, the axes are not consulted — a sleeping preview is
 * one that can be brought back by `up` alone.
 */

import type { Stack } from './spec.ts';
import type { Runner, RunResult } from './exec.ts';
import { subdomainEnv } from './subdomains.ts';
import { materializeCompose } from './autolabel.ts';
import { stackDeployCmd, stackLogsCmd, stackPsCmd, stackRmCmd } from './swarm.ts';

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
 * `runner.cwd` is the deployment directory (the registry sets it); the CLI sets none and docker runs
 * from the shell's directory, which is then where the compose file is read from and the derived one
 * written (beside it — see materializeCompose).
 */
async function fileArgsFor(spec: Stack, runner: Runner): Promise<string> {
  const { files } = await filesFor(spec, runner);
  return files.map((f) => `-f ${shq(f)}`).join(' ');
}

/** The compose files to pass, in order, plus what the swarm conversion changed (for the job log). */
async function filesFor(spec: Stack, runner: Runner): Promise<{ files: string[]; notes: string[] }> {
  const c = spec.compose!;
  // Nothing is written under --dry-run: a dry run must not have side effects, and the point of it is
  // to show what WOULD happen.
  if (runner.dryRun) return { files: [c.file, ...(c.overlays ?? [])], notes: [] };
  // The deployment directory under the API; the shell's directory under the CLI — which is also
  // where docker itself will resolve the `-f` path, so the two cannot disagree.
  const dir = runner.cwd ?? process.cwd();
  const { file, notes } = await materializeCompose({ dir, spec, runner });
  return { files: [file, ...(c.overlays ?? [])], notes };
}

const isSwarm = (spec: Stack) => spec.compose?.orchestrator === 'swarm';

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
  /** Where the swarm conversion's notes go. Optional: the CLI has no job log. */
  note: (line: string) => void = () => {},
): Promise<RunResult> {
  const c = spec.compose!;
  if (isSwarm(spec)) {
    const { files, notes } = await filesFor(spec, runner);
    for (const n of notes) note(`swarm: ${n}`);
    // `--prune` is `--remove-orphans`; profiles were resolved into the file by the conversion.
    return runner.run(stackDeployCmd(spec.stack, files), { env: composeEnv(spec, extraEnv), label: 'stack deploy' });
  }
  const cmd = `${await baseFor(spec, runner)} ${profileArgs(c.profiles)} up -d --remove-orphans`;
  // --remove-orphans drops services that were in a previous deploy but are not selected now, so a
  // relabel from "backend+frontend" to "backend" actually stops the frontend instead of orphaning it.
  return runner.run(cmd, { env: composeEnv(spec, extraEnv), label: 'compose up' });
}

export async function composeDown(spec: Stack, runner: Runner): Promise<RunResult> {
  const c = spec.compose!;
  if (isSwarm(spec)) {
    // `stack rm` takes nothing but the name; the conversion is not consulted and the labelled volumes
    // are removed afterwards (the `-v` equivalent — see swarm.ts for the worker-node ceiling).
    return runner.run(stackRmCmd(spec.stack, { volumes: true }), { env: composeEnv(spec), label: 'stack rm' });
  }
  // EVERY profile — see the file header.
  const cmd = `${await baseFor(spec, runner)} ${profileArgs(c.profiles)} down -v --remove-orphans`;
  return runner.run(cmd, { env: composeEnv(spec), label: 'compose down' });
}

/**
 * Put the project to sleep: containers and networks go, VOLUMES STAY. That is the whole difference
 * from `composeDown`, and it is a separate function rather than a flag so the `down -v` the leak tests
 * assert on cannot be weakened by a default.
 */
export async function composeSleep(spec: Stack, runner: Runner): Promise<RunResult> {
  const c = spec.compose!;
  if (isSwarm(spec)) {
    return runner.run(stackRmCmd(spec.stack, { volumes: false }), { env: composeEnv(spec), label: 'stack rm (sleep)' });
  }
  // Every profile, for the same reason `down` passes them: a profile left out is a network left behind.
  const cmd = `${await baseFor(spec, runner)} ${profileArgs(c.profiles)} down --remove-orphans`;
  return runner.run(cmd, { env: composeEnv(spec), label: 'compose down (sleep)' });
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
  if (isSwarm(spec)) {
    // `docker service logs` reads every node's tasks from the manager — the one thing swarm makes
    // easier. It has no `--until`; the API refuses that parameter in swarm mode rather than dropping it.
    return { cmd: stackLogsCmd(spec.stack, tail, service, opts), env: composeEnv(spec) };
  }
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
  if (isSwarm(spec)) return runner.run(stackPsCmd(spec.stack), { env: composeEnv(spec), label: 'stack ps' });
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
