/**
 * App-wide state: who is signed in, the two lists the shell polls, toasts, and theme preferences.
 *
 * Polled rather than streamed: the API's event stream is for one job's log, and a console that
 * refreshes every few seconds is what a person watching a deploy wants. `connection` is the honest
 * summary — a failed poll flips it, and the shell says so instead of showing stale rows as live.
 */
import { computed, reactive, ref, watch } from 'vue';
import type { DeploymentRow, Job } from '@samyx/preview-stacks-client';
import type { ToastItem } from '@hearth-ui/vue';
import { api, onUnauthorized } from './api';
import { clearToken, getToken, setToken } from './lib/token';

/** `role` is the signed-in account's role ('' for the host token); the one source controls are drawn from. */
export const auth = reactive({ checked: false, authed: false, username: '', root: false, id: 0, role: '', viaToken: false });

function signedOut(): void {
  stopPolling();
  auth.authed = false;
  auth.root = false;
  auth.id = 0;
  auth.role = '';
  auth.username = '';
  auth.viaToken = false;
}

// A session or token that dies mid-use lands on the sign-in page, not on a wall of failed panels.
onUnauthorized(() => {
  if (auth.authed) signedOut();
});
export const data = reactive({
  deployments: [] as DeploymentRow[],
  jobs: [] as Job[],
  loaded: false,
  error: '',
  connection: 'connecting' as 'connecting' | 'connected' | 'reconnecting' | 'failed',
});

// ── theme, remembered per browser ───────────────────────────────────────────────────────────────
const KEY = 'pstack.hearth.prefs';
type Prefs = { theme: string; mode: 'dark' | 'light' | 'system'; density: 'comfortable' | 'compact' };
function load(): Prefs {
  try {
    return { theme: 'sunset', mode: 'system', density: 'comfortable', ...JSON.parse(localStorage.getItem(KEY) ?? '{}') };
  } catch {
    return { theme: 'sunset', mode: 'system', density: 'comfortable' };
  }
}
export const prefs = reactive<Prefs>(load());
watch(prefs, (p) => {
  try {
    localStorage.setItem(KEY, JSON.stringify(p));
  } catch {
    /* private window: the choice just does not persist */
  }
});

// ── toasts ──────────────────────────────────────────────────────────────────────────────────────
export const toasts = ref<ToastItem[]>([]);
let seq = 0;
export function toast(title: string, tone: ToastItem['tone'] = 'neutral', description?: string): void {
  // A failure stays until dismissed: the one message that says why nothing happened must not time out. At most five show.
  toasts.value = [...toasts.value, { id: `t${++seq}`, title, tone, description, duration: tone === 'danger' ? 0 : 5000 }].slice(-5);
}
export function dismissToast(id: string): void {
  toasts.value = toasts.value.filter((t) => t.id !== id);
}

// ── polling ─────────────────────────────────────────────────────────────────────────────────────
let timer: ReturnType<typeof setInterval> | undefined;
let failures = 0;

export async function refresh(): Promise<void> {
  const [d, j] = await Promise.all([api.deployments(), api.jobs()]);
  if (d.status === 401 || j.status === 401) {
    signedOut();
    return;
  }
  if (!d.ok || !j.ok) {
    failures++;
    data.connection = failures > 2 ? 'failed' : 'reconnecting';
    data.error = d.body.error ?? j.body.error ?? 'The server did not answer.';
    return;
  }
  failures = 0;
  data.connection = 'connected';
  data.error = '';
  data.deployments = d.body.deployments ?? [];
  data.jobs = j.body.jobs ?? [];
  data.loaded = true;
}

export function startPolling(): void {
  stopPolling();
  void refresh();
  timer = setInterval(() => void refresh(), 6000);
}
export function stopPolling(): void {
  if (timer) clearInterval(timer);
  timer = undefined;
}

export const summary = computed(() => {
  const d = data.deployments;
  return {
    total: d.length,
    running: d.filter((x) => x.running === true).length,
    busy: d.filter((x) => x.busy === true).length,
    asleep: d.filter((x) => x.asleep).length,
    unresolved: d.filter((x) => x.unresolved).length,
    failedJobs: data.jobs.filter((j) => j.state === 'failed' || j.state === 'leaked').length,
  };
});

/** Reads who the server says is signed in. False when nobody is. */
export async function loadMe(): Promise<boolean> {
  const me = await api.me();
  if (me.status === 0 || me.status >= 500) {
    // No answer, or a proxy's 5xx: the server is away, which is not the same as signed out.
    data.connection = 'failed';
    data.error = me.body.error ?? 'The server did not answer.';
  } else {
    data.connection = 'connected';
    data.error = '';
  }
  if (me.ok) {
    auth.authed = true;
    auth.root = !!me.body.root;
    auth.id = me.body.user?.id ?? 0;
    auth.role = me.body.user?.role ?? '';
    auth.username = me.body.user?.username ?? (me.body.root ? 'root' : '');
    auth.viaToken = getToken() !== '';
  }
  return me.ok;
}

export async function start(): Promise<void> {
  const up = await loadMe();
  if (up) startPolling();
  auth.checked = true;
  // The server was away at load: ask again until it answers, so the page recovers without a reload.
  if (!up && data.connection === 'failed') {
    const retry = setInterval(async () => {
      const ok = await loadMe();
      if (data.connection === 'failed') return;
      clearInterval(retry);
      if (ok) startPolling();
    }, 4000);
  }
}

/** Stores the token, asks the server who it is, and keeps it only if the server accepts it. Returns the error line, or null. */
export async function signInWithToken(token: string): Promise<string | null> {
  setToken(token);
  if (await loadMe()) {
    startPolling();
    return null;
  }
  clearToken();
  return data.connection === 'failed' ? 'The server did not answer.' : 'That token was not accepted.';
}

/** Drops the stored token. A cookie session, if there is one, keeps working; otherwise the next call lands on sign-in. */
export async function forgetToken(): Promise<void> {
  clearToken();
  if (await loadMe()) auth.viaToken = false;
  else signedOut();
}

export async function signOut(): Promise<void> {
  await api.logout();
  clearToken();
  signedOut();
  data.deployments = [];
  data.jobs = [];
  data.loaded = false;
}
