import type { Tone } from '@hearth-ui/vue';

/** "3m ago". Coarse on purpose: the list refreshes every few seconds, seconds would flicker. */
export function ago(at: number | null | undefined, now = Date.now()): string {
  if (!at) return '—';
  const s = Math.max(0, Math.round((now - at) / 1000));
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

/** "12s" or "3m 07s". A job still running gets an ellipsis so it cannot read as final. */
export function took(start: number | null | undefined, end: number | null | undefined, now = Date.now()): string {
  if (!start) return '—';
  const s = Math.max(0, Math.round(((end ?? now) - start) / 1000));
  const t = s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`;
  return end ? t : `${t}…`;
}

/** "2026-07-28 05:17:00", viewer-local, sortable. */
export function stamp(ms: number | null | undefined): string {
  return ms ? new Date(ms).toLocaleString('sv') : '—';
}

/** "start-failed" to "Start failed". For enums only, never for ids. */
export function sentence(v: string | null | undefined): string {
  if (!v) return '—';
  const w = v.replace(/[-_]+/g, ' ');
  return w.charAt(0).toUpperCase() + w.slice(1);
}

export const actionLabel = (a: string | undefined): string => (a ? (ACTION_LABEL[a] ?? a) : '—');

export const ACTION_LABEL: Record<string, string> = {
  up: 'Deploy',
  verify: 'Verify',
  down: 'Tear down',
  sleep: 'Sleep',
  wake: 'Wake',
  'loki-apply': 'Loki settings',
};

export function runTone(state: string): Tone {
  return state === 'running' ? 'success' : state === 'busy' ? 'info' : state === 'unknown' ? 'warning' : 'neutral';
}

export function jobTone(state: string): Tone {
  if (state === 'ok') return 'success';
  if (state === 'running') return 'info';
  if (state === 'failed' || state === 'leaked') return 'danger';
  if (state === 'queued') return 'warning';
  return 'neutral';
}

export function containerTone(state: string, health: string | null): Tone {
  if (health === 'unhealthy') return 'danger';
  if (state === 'running') return 'success';
  if (state === 'exited') return 'neutral';
  return state === 'no task running' ? 'danger' : 'warning';
}
