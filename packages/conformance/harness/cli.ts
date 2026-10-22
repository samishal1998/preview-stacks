/**
 * Run `pstack <args>` for the selected implementation and capture everything.
 *
 * cwd defaults to packages/pstack so `examples/preview.yml` and relative spec paths resolve the
 * same way in every mode — the CLI goldens carry that cwd explicitly.
 */
import { CLI_CWD, cleanEnv, cliArgv } from './impl.ts';

export type CliResult = { stdout: string; stderr: string; code: number; all: string };

export async function runCli(
  args: string[],
  o: { cwd?: string; env?: Record<string, string | undefined>; stdin?: string; pathPrefix?: string } = {},
): Promise<CliResult> {
  const env: Record<string, string | undefined> = {
    ...cleanEnv(),
    PATH: o.pathPrefix ? `${o.pathPrefix}:${process.env.PATH ?? ''}` : process.env.PATH,
    // `init` writes a systemd drop-in for docker.service when it runs as root on a systemd host.
    // A test run must never touch the machine it runs on.
    PSTACK_NO_OOM_PROTECT: '1',
    ...o.env,
  };
  for (const k of Object.keys(env)) if (env[k] === undefined) delete env[k];
  const proc = Bun.spawn(cliArgv(args), {
    cwd: o.cwd ?? CLI_CWD,
    env: env as Record<string, string>,
    stdin: o.stdin !== undefined ? new TextEncoder().encode(o.stdin) : 'ignore',
    stdout: 'pipe',
    stderr: 'pipe',
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]);
  return { stdout, stderr, code, all: stdout + stderr };
}
