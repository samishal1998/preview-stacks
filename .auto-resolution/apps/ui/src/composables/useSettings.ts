/**
 * App settings — one module-level reactive object, persisted to localStorage.
 *
 * A module singleton rather than a store library or a provide/inject tree: there is exactly one of
 * these per tab, every view reads it, and nothing needs time-travel or devtools. The API client
 * imports it directly, which is why it lives in `composables/` but exports a plain object rather
 * than a `useX()` factory — a factory would imply per-component state that must not exist.
 */

import { reactive, watch } from 'vue';

export type Theme = 'system' | 'dark' | 'light';
/** 'system' honours `prefers-reduced-motion`; the other two override it in both directions. */
export type Motion = 'system' | 'full' | 'none';

const KEY = {
  token: 'pstack.ui.token',
  apiBase: 'pstack.ui.apiBase',
  theme: 'pstack.ui.theme',
  motion: 'pstack.ui.motion',
} as const;

/** Reading storage can throw in private mode. A missing setting is never worth a blank page. */
function read(key: string, fallback: string): string {
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

function write(key: string, value: string): void {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    /* private mode / quota — the value still works for this page load */
  }
}

const theme = read(KEY.theme, 'system');
const motion = read(KEY.motion, 'system');

export const settings = reactive({
  /**
   * The bearer token. Held in localStorage because an operator should not retype it on every
   * navigation — the same tradeoff the basic UI makes. It is only ever attached to POST/PUT/DELETE.
   */
  token: read(KEY.token, ''),
  /** '' = same origin, which is how the container serves it (nginx proxies /api/ to pstack). */
  apiBase: read(KEY.apiBase, ''),
  theme: (theme === 'dark' || theme === 'light' ? theme : 'system') as Theme,
  motion: (motion === 'full' || motion === 'none' ? motion : 'system') as Motion,
});

/**
 * Mirror the two presentation settings onto <html> so CSS can win in BOTH directions: a manual
 * "light" must beat a dark OS preference and vice-versa, which a media query alone cannot do.
 * `index.html` applies the same two attributes before first paint; this keeps them in step after.
 */
function applyDocumentAttributes(): void {
  const el = document.documentElement;
  if (settings.theme === 'system') delete el.dataset.theme;
  else el.dataset.theme = settings.theme;
  if (settings.motion === 'system') delete el.dataset.motion;
  else el.dataset.motion = settings.motion;
}

watch(() => settings.token, (v) => write(KEY.token, v));
watch(() => settings.apiBase, (v) => write(KEY.apiBase, v));
watch(
  () => [settings.theme, settings.motion],
  () => {
    write(KEY.theme, settings.theme === 'system' ? '' : settings.theme);
    write(KEY.motion, settings.motion === 'system' ? '' : settings.motion);
    applyDocumentAttributes();
  },
);

applyDocumentAttributes();
