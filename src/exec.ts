/**
 * Running things. One place, so dry-run and output capture are impossible to forget.
 *
 * Hooks are shell strings, run via `bash -c`, with the resolved spec env injected. That is a
 * deliberate trade: this tool orchestrates other people's CLIs (docker, gcloud, curl, psql), and a
 * structured arg-array API would just push quoting into YAML. Hooks come from a spec file in your
 * own repo — the same trust level as a CI workflow — so this is not a sandbox boundary.
 */

export type RunResult = {
  ok: boolean;
  code: number;
  stdout: string;
  stderr: string;
  /** True when skipped because of --dry-run. */
  skipped: boolean;
};

export type Runner = {
  run(cmd: string, opts?: { env?: Record<string, string>; label?: string }): Promise<RunResult>;
  readonly dryRun: boolean;
};

export type LogLevel = 'quiet' | 'normal' | 'verbose';

export function createRunner(opts: {
  dryRun: boolean;
  level?: LogLevel;
  cwd?: string;
  baseEnv?: Record<string, string>;
}): Runner {
  const level = opts.level ?? 'normal';
  return {
    dryRun: opts.dryRun,
    async run(cmd, o = {}) {
      const label = o.label ?? cmd;
      if (opts.dryRun) {
        if (level !== 'quiet') console.log(`  [dry-run] ${label}`);
        return { ok: true, code: 0, stdout: '', stderr: '', skipped: true };
      }
      if (level === 'verbose') console.log(`  $ ${cmd}`);

      const proc = Bun.spawn(['bash', '-c', cmd], {
        cwd: opts.cwd,
        env: { ...opts.baseEnv, ...o.env },
        stdout: 'pipe',
        stderr: 'pipe',
      });
      const [stdout, stderr, code] = await Promise.all([
        new Response(proc.stdout).text(),
        new Response(proc.stderr).text(),
        proc.exited,
      ]);
      if (level === 'verbose') {
        if (stdout.trim()) console.log(indent(stdout));
        if (stderr.trim()) console.log(indent(stderr));
      }
      return { ok: code === 0, code, stdout, stderr, skipped: false };
    },
  };
}

function indent(s: string): string {
  return s
    .trimEnd()
    .split('\n')
    .map((l) => `    | ${l}`)
    .join('\n');
}

/**
 * Capture `KEY=VALUE` lines from a provision hook's stdout.
 *
 * This is how a provisioned resource hands its coordinates to the rest of the stack — a freshly
 * created database emits `DATABASE_URL=postgres://…` and compose picks it up as an env var. Only
 * SHOUT_CASE keys are captured so ordinary log chatter is ignored; everything else on stdout is
 * free-form.
 */
export function captureOutputs(stdout: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of stdout.split('\n')) {
    const m = /^([A-Z_][A-Z0-9_]*)=(.*)$/.exec(line.trim());
    // `noUncheckedIndexedAccess` widens capture groups to `string | undefined`; a matched group 1
    // is always present, but narrow explicitly rather than asserting.
    const key = m?.[1];
    if (key !== undefined) out[key] = m?.[2] ?? '';
  }
  return out;
}

/** Values that look like credentials, masked in logs. Best-effort, not a security boundary. */
export function maskSecrets(text: string, secrets: string[]): string {
  let out = text;
  for (const s of secrets) {
    if (s && s.length >= 8) out = out.split(s).join('***');
  }
  return out;
}
