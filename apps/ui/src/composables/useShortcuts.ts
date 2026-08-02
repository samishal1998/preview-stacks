/**
 * Global keyboard shortcuts.
 *
 * One listener on the document rather than per-view handlers, because the navigation shortcuts
 * must work from every view. Two rules keep it out of the way:
 *   - a keystroke inside a field is text, never a command (except Escape);
 *   - `g` is a PREFIX, not a chord: `g` then `d`. A prefix that never resolves times out, so a
 *     stray `g` cannot swallow the next real keystroke.
 */

import { onMounted, onUnmounted, ref } from 'vue';
import { useRouter } from 'vue-router';

const PREFIX_TIMEOUT_MS = 900;

/** True for anything that eats text: inputs, textareas, selects, contenteditable. */
function isTyping(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el) return false;
  const tag = el.tagName;
  return (
    tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable === true
  );
}

export function useShortcuts() {
  const router = useRouter();
  const sheetOpen = ref(false);
  let prefixAt = 0;
  let prefix = '';

  const onKey = (e: KeyboardEvent): void => {
    // Escape closes whatever is open and gives the field back — the one key that works while typing.
    if (e.key === 'Escape') {
      if (sheetOpen.value) {
        sheetOpen.value = false;
        e.preventDefault();
      }
      if (isTyping(e.target)) (e.target as HTMLElement).blur();
      return;
    }
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    if (isTyping(e.target)) return;

    const now = Date.now();
    if (prefix === 'g' && now - prefixAt < PREFIX_TIMEOUT_MS) {
      prefix = '';
      const to = { d: '/deployments', j: '/jobs', h: '/', s: '/settings', n: '/submit' }[e.key];
      if (to) {
        e.preventDefault();
        void router.push(to);
      }
      return;
    }

    if (e.key === 'g') {
      prefix = 'g';
      prefixAt = now;
      return;
    }
    prefix = '';

    if (e.key === '?') {
      e.preventDefault();
      sheetOpen.value = !sheetOpen.value;
      return;
    }
    if (e.key === '/') {
      // Views that have a search box mark it `data-search`. Nothing to focus is not an error —
      // the key simply does nothing on a view without one.
      const el = document.querySelector<HTMLInputElement>('[data-search]');
      if (el) {
        e.preventDefault();
        el.focus();
        el.select();
      }
    }
  };

  onMounted(() => document.addEventListener('keydown', onKey));
  onUnmounted(() => document.removeEventListener('keydown', onKey));

  return { sheetOpen };
}

/** The sheet's own contents, so the list and the handler above cannot drift apart. */
export const SHORTCUTS: Array<{ keys: string; what: string }> = [
  { keys: '⌘K / Ctrl-K', what: 'Open the command palette — jump to any deployment or page' },
  { keys: '?', what: 'Open or close this sheet' },
  { keys: 'g h', what: 'Go to the dashboard' },
  { keys: 'g d', what: 'Go to deployments' },
  { keys: 'g j', what: 'Go to jobs' },
  { keys: 'g n', what: 'Go to submit' },
  { keys: 'g s', what: 'Go to settings' },
  { keys: '/', what: 'Focus the search box on this view' },
  { keys: 'Esc', what: 'Close this sheet, or leave the field you are typing in' },
];
