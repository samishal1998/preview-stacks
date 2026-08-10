/**
 * Formatting shared across views. All times are the VIEWER's local time, with no zone marker,
 * because everything on the page — log lines, job stamps, deployment timestamps — comes from the
 * same clock and mixing a rendered zone into one of them is how two timestamps stop comparing.
 */

/** ISO 8601 shaped and sortable: `2026-07-28 05:17:00`. `sv` is the locale that renders it. */
export function stamp(ms: number | undefined): string {
  return ms ? new Date(ms).toLocaleString('sv') : '—';
}

/** Just the clock, for a log line where the date is the same for every row. */
export function hhmmss(ms: number | undefined): string {
  return ms ? new Date(ms).toTimeString().slice(0, 8) : '--:--:--';
}

/** `12s` / `3m07s`. A running job gets an ellipsis so a stale number cannot read as a final one. */
export function took(startedAt: number | undefined, endedAt?: number): string {
  if (!startedAt) return '—';
  const s = Math.max(0, Math.round(((endedAt ?? Date.now()) - startedAt) / 1000));
  const text = s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${String(s % 60).padStart(2, '0')}s`;
  return endedAt ? text : `${text}…`;
}

/** `2 minutes ago`, for an activity feed where the exact second is noise. */
export function ago(ms: number | undefined): string {
  if (!ms) return '—';
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 45) return 'just now';
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}

/**
 * The user-facing name of an action.
 *
 * The API's verbs (`up`, `down`, `verify`) are the CLI's and the spec's vocabulary, and they stay in
 * both. But a BUTTON should say what it does, so the same mapping is used everywhere a person reads
 * an action — buttons, the job list, a job's own heading — rather than only on the buttons, which
 * would leave someone clicking "Tear down" and then reading about a job called `down`.
 */
const ACTION_LABELS: Record<string, string> = {
  up: 'Deploy',
  down: 'Tear down',
  verify: 'Verify',
};

export function actionLabel(action: string | undefined): string {
  if (!action) return '—';
  return ACTION_LABELS[action] ?? action;
}

/**
 * An API enum, rendered for a person: `isolated` → `Isolated`, `start-failed` → `Start failed`.
 *
 * ONE helper rather than capitalising at each render site, because the two halves have to stay
 * apart: the VALUE stays lowercase everywhere it is compared, stored, sent or put in a class name,
 * and only the rendered text changes. Capitalising in place — `state === 'Running'` — is how a badge
 * starts looking right and a comparison silently stops matching.
 *
 * Sentence case, not Title Case: `Start failed`, not `Start Failed`. Title Case on a status reads
 * like a proper noun, and these are descriptions.
 *
 * Identifiers are NOT sentences and must never come through here — a stack name, container name,
 * deployment id, file name, env var or hostname is a literal, and capitalising one produces a value
 * that does not exist.
 */
export function sentence(value: string | null | undefined): string {
  if (!value) return '—';
  const words = value.replace(/[-_]+/g, ' ');
  return words.charAt(0).toUpperCase() + words.slice(1);
}
