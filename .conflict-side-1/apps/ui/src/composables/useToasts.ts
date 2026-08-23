/**
 * Toasts — a module-level stack, so any view can raise one without prop-drilling a handler.
 *
 * Used for things that happen ASIDE from what you are looking at: a job started and you were
 * navigated elsewhere, a job you started finished, a request was refused. Never used as the ONLY
 * place an error appears — a toast is dismissible and time-limited, and a 409 refusal or a
 * SpecError must stay on the page where the operator can read it twice.
 */

import { reactive } from 'vue';

export type ToastKind = 'info' | 'ok' | 'warn' | 'error';

export type Toast = {
  id: number;
  kind: ToastKind;
  text: string;
  /** Optional in-app destination, e.g. the job the action just started. */
  to?: string;
  toLabel?: string;
};

let seq = 0;
export const toasts = reactive<Toast[]>([]);

export function dismissToast(id: number): void {
  const i = toasts.findIndex((t) => t.id === id);
  if (i >= 0) toasts.splice(i, 1);
}

export function toast(
  kind: ToastKind,
  text: string,
  opts: { to?: string; toLabel?: string; ms?: number } = {},
): number {
  const id = ++seq;
  toasts.push({ id, kind, text, to: opts.to, toLabel: opts.toLabel });
  // Errors stay until dismissed. Auto-hiding the one message that explains why an action did
  // nothing is how an operator ends up re-running it.
  const ms = opts.ms ?? (kind === 'error' ? 0 : 6000);
  if (ms > 0) setTimeout(() => dismissToast(id), ms);
  // Keep the stack short enough to stay readable on a phone.
  while (toasts.length > 5) toasts.shift();
  return id;
}
