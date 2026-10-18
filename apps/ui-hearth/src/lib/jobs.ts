/** Job facts the wire does not carry: terminal states, why a job waits, what replaced it, step outcomes. */
import type { Job, JobState, StepResult } from '@samyx/preview-stacks-client';

/** Terminal is forever. Not `state !== 'running'`: a queued job has not started. */
export function isTerminal(state: JobState): boolean {
  return state !== 'queued' && state !== 'running';
}

export type Wait = { kind: 'stack'; blocker: Job } | { kind: 'slot'; running: number } | { kind: 'unknown' };

/** Why a queued job has not started, inferred from the polled list. */
export function waitReason(job: Job, jobs: Job[]): Wait {
  const others = jobs.filter((j) => j.id !== job.id);
  const blocker = others.find((j) => j.state === 'running' && j.stack === job.stack);
  if (blocker) return { kind: 'stack', blocker };
  const running = others.filter((j) => j.state === 'running').length;
  return running > 0 ? { kind: 'slot', running } : { kind: 'unknown' };
}

export function waitText(w: Wait): string {
  if (w.kind === 'stack') return 'Behind this stack’s own job.';
  if (w.kind === 'slot') return `Host job limit reached, ${w.running} running.`;
  return 'Waiting its turn.';
}

/** The job that took a superseded one's place (list is newest first), or null once it aged out. */
export function supersededBy(job: Job, jobs: Job[]): Job | null {
  const i = jobs.findIndex((j) => j.id === job.id);
  if (i < 0) return null;
  return jobs.slice(0, i).reverse().find((j) => j.stack === job.stack) ?? null;
}

/** Where the job's stack lives: its deployment, Control for the control plane's own, or nowhere. */
export function jobTarget(job: Pick<Job, 'deployment'>): string | null {
  if (job.deployment) return `/deployments/${encodeURIComponent(job.deployment)}`;
  return job.deployment === null ? '/control' : null;
}

export type StepState = 'ok' | 'unverifiable' | 'leaked' | 'failed';

/** `unverifiable` first: a skipped step is not enough, a dry run skips too. */
export function stepState(s: StepResult): StepState {
  if (s.message?.startsWith('unverifiable')) return 'unverifiable';
  if (!s.ok) return s.phase === 'assert_gone' ? 'leaked' : 'failed';
  return 'ok';
}

export function stepText(s: StepResult): string {
  if (s.message) return s.message;
  if (s.skipped) return 'skipped';
  return stepState(s) === 'ok' ? 'ok' : `exit ${s.code}`;
}

/** "failed: exit 1"; never "ok: ok" or "unverifiable: unverifiable: ...". */
export function stepLine(s: StepResult): string {
  const state = stepState(s);
  const text = stepText(s);
  return text === state || text.startsWith(state) ? text : `${state}: ${text}`;
}

export const leakedAxes = (steps: StepResult[]): string[] => steps.filter((s) => s.phase === 'assert_gone' && !s.ok).map((s) => s.axis);
export const countUnverifiable = (steps: StepResult[]): number => steps.filter((s) => stepState(s) === 'unverifiable').length;

export type LogLine = { seq: number; at: number; level: string; message: string };

/** Add a streamed line unless that seq is already shown (replay after a reconnect). Order kept. */
export function addLine(lines: LogLine[], line: LogLine): LogLine[] {
  return lines.some((l) => l.seq === line.seq) ? lines : [...lines, line];
}

/** Viewer rows: the clock per line, the date only where the day changes. */
export function logEntries(lines: LogLine[]) {
  let day = '';
  return lines.map((l) => {
    const d = new Date(l.at);
    const thisDay = d.toLocaleDateString('sv');
    const clock = d.toTimeString().slice(0, 8);
    const timestamp = thisDay === day ? clock : `${thisDay} ${clock}`;
    day = thisDay;
    const level: 'info' | 'warning' | 'error' = l.level === 'error' ? 'error' : l.level === 'warn' || l.level === 'warning' ? 'warning' : 'info';
    return { id: String(l.seq), message: l.message, timestamp, level };
  });
}
