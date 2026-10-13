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
import { api } from './api';

export const auth = reactive({ checked: false, authed: false, username: '', root: false });
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
  toasts.value = [...toasts.value, { id: `t${++seq}`, title, tone, description, duration: tone === 'danger' ? 9000 : 5000 }];
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
    auth.authed = false;
    stopPolling();
    return;
  }
  if (!d.ok || !j.ok) {
    failures++;
    data.connection = failures > 2 ? 'failed' : 'reconnecting';
    data.error = d.body.error ?? j.body.error ?? 'could not load';
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

export async function start(): Promise<void> {
  const me = await api.me();
  if (me.status === 0) {
    data.connection = 'failed';
    data.error = me.body.error ?? '';
  } else if (me.ok) {
    auth.authed = true;
    auth.root = !!me.body.root;
    auth.username = me.body.user?.username ?? (me.body.root ? 'root' : '');
    startPolling();
  }
  auth.checked = true;
}

export async function signOut(): Promise<void> {
  await api.logout();
  stopPolling();
  auth.authed = false;
  data.deployments = [];
  data.jobs = [];
  data.loaded = false;
}
