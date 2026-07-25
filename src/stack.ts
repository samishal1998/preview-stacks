/**
 * Lifecycle orchestration. The whole point of this file is the asymmetry between `down` and
 * `verify`:
 *
 *   down   is BEST-EFFORT — a resource may already be gone, a network may be busy, an API may
 *          flake. Aborting halfway leaves MORE garbage than continuing, so every failure is
 *          recorded and teardown carries on.
 *   verify is STRICT — it asserts each axis is actually gone and exits non-zero if not.
 *
 * Homegrown teardown scripts almost always implement the first half and skip the second, which is
 * why they leak: `docker compose down -v` never removes images, and a `down` that omits a profile
 * silently leaves that profile's network behind. `verify` is the part that catches it.
 */

import { type Stack } from './spec.ts';
import { captureOutputs, type Runner } from './exec.ts';
import { composeDown, composeUp, composePs } from './compose.ts';
import { consoleSink, type Sink } from './log.ts';

export type StepResult = {
  axis: string;
  phase: 'up' | 'down' | 'assert_gone' | 'assert_live' | 'compose';
  ok: boolean;
  code: number;
  message?: string;
  skipped: boolean;
};

export type Outcome = {
  ok: boolean;
  steps: StepResult[];
  /** Env accumulated from axis outputs during `up`. */
  outputs: Record<string, string>;
};

const ok = (r: Outcome) => r.steps.every((s) => s.ok);

/**
 * Bring a stack up: provision every axis in declaration order, then compose up.
 *
 * Fails FAST — unlike teardown, a half-provisioned stack should not proceed to deploy. If the
 * database axis fails there is no point starting the app that migrates it, and continuing would
 * produce a confusing app-level error instead of the real one.
 */
export async function up(spec: Stack, runner: Runner, sink: Sink = consoleSink()): Promise<Outcome> {
  const steps: StepResult[] = [];
  const outputs: Record<string, string> = {};

  for (const axis of spec.axes) {
    if (axis.up) {
      sink.emit('step', `→ up: ${axis.name}`);
      const env = { ...spec.env, ...outputs, STACK: spec.stack };
      const r = await runner.run(axis.up, { env, label: `up ${axis.name}` });
      steps.push({
        axis: axis.name,
        phase: 'up',
        ok: r.ok,
        code: r.code,
        skipped: r.skipped,
        message: r.ok ? undefined : firstLine(r.stderr || r.stdout),
      });
      if (!r.ok) return { ok: false, steps, outputs };
      Object.assign(outputs, captureOutputs(r.stdout));
    }

    // Prove the resource really exists. A provision hook that exits 0 without creating anything
    // (a typo'd API path returning 200 on a list endpoint, say) otherwise surfaces much later as
    // an opaque connection error.
    if (axis.assert_live) {
      const env = { ...spec.env, ...outputs, STACK: spec.stack };
      const r = await runner.run(axis.assert_live, { env, label: `assert_live ${axis.name}` });
      steps.push({
        axis: axis.name,
        phase: 'assert_live',
        ok: r.ok,
        code: r.code,
        skipped: r.skipped,
        message: r.ok ? undefined : `provisioned but assert_live failed — resource missing?`,
      });
      if (!r.ok) return { ok: false, steps, outputs };
    }
  }

  if (spec.compose) {
    sink.emit('step', `→ compose up (${spec.compose.profiles.join(', ') || 'no profiles'})`);
    const r = await composeUp(spec, runner, outputs);
    steps.push({ axis: '(compose)', phase: 'compose', ok: r.ok, code: r.code, skipped: r.skipped,
      message: r.ok ? undefined : firstLine(r.stderr || r.stdout) });
  }

  return { ok: ok({ ok: true, steps, outputs }), steps, outputs };
}

/**
 * Tear a stack down: compose down (with EVERY profile), then destroy axes in REVERSE order, then
 * verify. Reverse order matters for the same reason as `up`'s forward order — dependents before
 * dependencies.
 *
 * @param verify run assert_gone after destroying (default true). The reason `down` verifies by
 *               default rather than leaving it to a separate command: a teardown that silently
 *               half-worked is the failure this tool exists to catch.
 */
export async function down(
  spec: Stack,
  runner: Runner,
  opts: { verify?: boolean } = {},
  sink: Sink = consoleSink(),
): Promise<Outcome> {
  const steps: StepResult[] = [];
  const doVerify = opts.verify ?? true;

  if (spec.compose) {
    sink.emit('step', '→ compose down (all profiles)');
    const r = await composeDown(spec, runner);
    // Best-effort: a missing project directory or an already-removed stack is normal on a retry.
    steps.push({ axis: '(compose)', phase: 'compose', ok: true, code: r.code, skipped: r.skipped,
      message: r.ok ? undefined : `non-fatal: ${firstLine(r.stderr || r.stdout)}` });
  }

  for (const axis of [...spec.axes].reverse()) {
    if (!axis.down) continue;
    sink.emit('step', `→ down: ${axis.name}`);
    const r = await runner.run(axis.down, { env: { ...spec.env, STACK: spec.stack }, label: `down ${axis.name}` });
    // Recorded but never fatal — see the file header.
    steps.push({
      axis: axis.name,
      phase: 'down',
      ok: true,
      code: r.code,
      skipped: r.skipped,
      message: r.ok ? undefined : `non-fatal: ${firstLine(r.stderr || r.stdout)}`,
    });
  }

  if (doVerify) {
    const v = await verify(spec, runner, sink);
    steps.push(...v.steps);
  }

  return { ok: ok({ ok: true, steps, outputs: {} }), steps, outputs: {} };
}

/**
 * Assert every axis is gone. This is the leak gate — exit non-zero means something survived
 * teardown, which in CI is exactly what you want to fail on.
 *
 * Axes without `assert_gone` are reported as `unverifiable` rather than passing silently, so a
 * green `verify` genuinely means "everything that CAN be checked was checked".
 */
export async function verify(spec: Stack, runner: Runner, sink: Sink = consoleSink()): Promise<Outcome> {
  const steps: StepResult[] = [];
  sink.emit('step', '→ verify (asserting resources are gone)');

  for (const axis of spec.axes) {
    if (!axis.assert_gone) {
      steps.push({
        axis: axis.name,
        phase: 'assert_gone',
        ok: true,
        code: 0,
        skipped: true,
        message: 'unverifiable: no assert_gone defined',
      });
      continue;
    }
    const r = await runner.run(axis.assert_gone, {
      env: { ...spec.env, STACK: spec.stack },
      label: `assert_gone ${axis.name}`,
    });
    steps.push({
      axis: axis.name,
      phase: 'assert_gone',
      ok: r.ok,
      code: r.code,
      skipped: r.skipped,
      message: r.ok ? undefined : 'LEAKED: resource still present after teardown',
    });
  }

  return { ok: steps.every((s) => s.ok), steps, outputs: {} };
}

/** What is currently running for this stack, straight from compose. */
export async function status(spec: Stack, runner: Runner): Promise<string> {
  if (!spec.compose) return '(no compose section in spec)';
  const r = await composePs(spec, runner);
  return r.stdout.trim() || '(nothing running)';
}

function firstLine(s: string): string {
  return (s.trim().split('\n')[0] ?? '').slice(0, 300);
}

/** Render an Outcome as a compact report. */
export function report(o: Outcome): string {
  const lines = o.steps.map((s) => {
    const mark = s.skipped && s.message?.startsWith('unverifiable') ? '?' : s.ok ? '✓' : '✗';
    const msg = s.message ? `  — ${s.message}` : '';
    return `  ${mark} ${s.phase.padEnd(12)} ${s.axis}${msg}`;
  });
  const leaks = o.steps.filter((s) => s.phase === 'assert_gone' && !s.ok).length;
  const unverifiable = o.steps.filter((s) => s.message?.startsWith('unverifiable')).length;
  const tail: string[] = [];
  if (leaks > 0) tail.push(`${leaks} leaked resource(s)`);
  if (unverifiable > 0) tail.push(`${unverifiable} unverifiable axis/axes`);
  return [...lines, tail.length ? `  ${tail.join(', ')}` : ''].filter(Boolean).join('\n');
}
