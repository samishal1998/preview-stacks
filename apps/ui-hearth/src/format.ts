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

export function took(start: number | null | undefined, end: number | null | undefined): string {
  if (!start || !end) return '—';
  const s = Math.max(0, Math.round((end - start) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

export const ACTION_LABEL: Record<string, string> = {
  up: 'Deploy',
  verify: 'Verify',
  down: 'Tear down',
  sleep: 'Sleep',
  wake: 'Wake',
  'loki-apply': 'Apply logging',
};

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
