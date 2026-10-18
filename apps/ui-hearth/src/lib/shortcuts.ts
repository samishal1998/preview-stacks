/** Global keys: `?` opens the list, `g` then a letter goes to a page. `g` expires, so a stray one cannot eat a later key. */
export const PREFIX_MS = 900;
export const GO: Record<string, string> = { h: '/', d: '/deployments', j: '/jobs', n: '/submit', s: '/settings' };

export const SHORTCUTS: Array<{ keys: string; what: string }> = [
  { keys: 'Ctrl K', what: 'Search' },
  { keys: '?', what: 'This list' },
  { keys: 'g h', what: 'Overview' },
  { keys: 'g d', what: 'Deployments' },
  { keys: 'g j', what: 'Jobs' },
  { keys: 'g n', what: 'Submit' },
  { keys: 'g s', what: 'Settings' },
  { keys: '/', what: 'Filter this page' },
];

export type KeyInfo = { key: string; typing: boolean; mod: boolean };
export type Step = { pendingAt: number; go?: string; help?: boolean };

/** One keystroke in, the next prefix state and at most one effect out. */
export function keyStep(pendingAt: number, e: KeyInfo, now: number): Step {
  if (e.typing || e.mod) return { pendingAt: 0 };
  if (pendingAt && now - pendingAt < PREFIX_MS) return GO[e.key] ? { pendingAt: 0, go: GO[e.key] } : { pendingAt: 0 };
  if (e.key === 'g') return { pendingAt: now };
  if (e.key === '?') return { pendingAt: 0, help: true };
  return { pendingAt: 0 };
}

export function isTyping(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  return !!el?.closest?.('input, textarea, select, [contenteditable]');
}
