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
  /**
   * Aborting this stops the CURRENT command and refuses every later one.
   *
   * A job is a sequence of shell hooks, and there was no other handle on them: without a signal the
   * only way to stop a wedged 40-minute image build was to restart the control plane, which loses
   * every other job's transcript with it.
   */
  readonly signal?: AbortSignal;
  /**
   * The directory commands run in — the deployment directory for a registry-driven run, the spec's
   * own directory for a CLI one.
   *
   * Exposed (not just used internally) because compose needs it: pstack writes a derived compose file
   * next to the submitted one and passes that to `-f`, and the alternative to reading it from here was
   * threading a `dir` argument through `up`/`down`/`verify` and four compose helpers.
   */
  readonly cwd?: string;
};

export type LogLevel = 'quiet' | 'normal' | 'verbose';

export function createRunner(opts: {
  dryRun: boolean;
  level?: LogLevel;
  cwd?: string;
  baseEnv?: Record<string, string>;
  signal?: AbortSignal;
}): Runner {
  const level = opts.level ?? 'normal';
  return {
    dryRun: opts.dryRun,
    cwd: opts.cwd,
    signal: opts.signal,
    async run(cmd, o = {}) {
      const label = o.label ?? cmd;
      // Refuse AFTER a cancel as firmly as during one. Teardown is best-effort and keeps going past
      // failures (see stack.ts), so without this check a cancelled `down` would carry on running
      // every remaining axis hook — the operator pressed stop and watched it continue.
      if (opts.signal?.aborted) {
        return { ok: false, code: 130, stdout: '', stderr: 'cancelled', skipped: false };
      }
      if (opts.dryRun) {
        // Verbose shows the REAL command, not just the label. A dry-run exists to answer "what
        // exactly would run", and a label like `compose up` cannot answer it — you cannot check
        // the project name, the profile flags or the file path against a summary.
        if (level === 'verbose') console.log(`  [dry-run] $ ${cmd}`);
        else if (level !== 'quiet') console.log(`  [dry-run] ${label}`);
        return { ok: true, code: 0, stdout: '', stderr: '', skipped: true };
      }
      if (level === 'verbose') console.log(`  $ ${cmd}`);

      const proc = Bun.spawn(['bash', '-c', cmd], {
        cwd: opts.cwd,
        env: { ...opts.baseEnv, ...o.env },
        stdout: 'pipe',
        stderr: 'pipe',
      });
      // SIGTERM first, so a hook can trap it and clean up. Registered per-command and removed in
      // `finally`, or a long job accumulates one listener per step on the same signal.
      const kill = () => {
        try {
          proc.kill('SIGTERM');
        } catch {
          /* already gone — racing between exiting and being killed is not an error */
        }
      };
      opts.signal?.addEventListener('abort', kill, { once: true });
      let stdout: string;
      let stderr: string;
      let code: number;
      try {
        [stdout, stderr, code] = await Promise.all([
          new Response(proc.stdout).text(),
          new Response(proc.stderr).text(),
          proc.exited,
        ]);
      } finally {
        opts.signal?.removeEventListener('abort', kill);
      }
      if (opts.signal?.aborted) {
        return { ok: false, code: code || 130, stdout, stderr: stderr || 'cancelled', skipped: false };
      }
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
