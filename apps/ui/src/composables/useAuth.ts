/**
 * Who is signed in, shared app-wide.
 *
 * Auth state is checked ONCE at startup (`/api/auth/me`) and then maintained by the actions that
 * change it — login, logout, and any request coming back 401. The 401 hook matters more than the
 * initial check: a session can expire mid-use, and the first sign must be a redirect to the login
 * page, not a wall of failed panels.
 *
 * Three signed-in shapes, mirroring the server's principals:
 *   - a user session (cookie)      → `user` is set
 *   - a bearer token in Settings   → `root` (PSTACK_TOKEN) or `user` (a personal token)
 *   - loopback dev mode, no token  → `root`
 */
import { reactive } from 'vue';
import { api, onUnauthorized } from '../api/client';
import { router } from '../router';

export const authState = reactive({
  /** The startup check has completed — routing decisions before this would flicker. */
  checked: false,
  authed: false,
  root: false,
  user: null as { id: number; username: string; role: string; email?: string | null } | null,
  /** From /api/health: whether any account exists — decides "sign in" vs "bootstrap first". */
  hasUsers: null as boolean | null,
  /**
   * From /api/health: the ENABLED identity providers, or null when there are none. Read BEFORE
   * authenticating — the login page needs one button per provider and nothing more: what to write
   * on it (`label`), the key `/api/auth/sso/start?provider=` takes, and `preset` for the brand
   * mark (`''` for a bare OIDC issuer).
   */
  sso: null as { providers: Array<{ key: string; label: string; preset: string }> } | null,
});

export async function checkAuth(): Promise<void> {
  const health = await api.get<{
    hasUsers?: boolean;
    sso?: { providers: Array<{ key: string; label: string; preset: string }> } | null;
  }>('/api/health');
  if (health.ok) {
    authState.hasUsers = health.body.hasUsers ?? null;
    authState.sso = health.body.sso ?? null;
  }

  const me = await api.getAuthed<{ root: boolean; user?: { id: number; username: string; role: string; email?: string | null } }>(
    '/api/auth/me',
  );
  /*
   * Status 0 is "the server did not answer", not "you are signed out" — a control plane restarting
   * for two seconds must not bounce a valid session to the login page. Leaving `checked` false
   * makes the router guard retry on the next navigation instead of caching the outage as a verdict.
   */
  if (me.status === 0) return;
  authState.checked = true;
  if (me.ok) {
    authState.authed = true;
    authState.root = me.body.root === true;
    authState.user = me.body.user ?? null;
  } else {
    authState.authed = false;
    authState.root = false;
    authState.user = null;
  }
}

export async function login(username: string, password: string): Promise<string | null> {
  const r = await api.post<{ user: { id: number; username: string; role: string; email?: string | null } }>(
    '/api/auth/login',
    { username, password },
  );
  if (!r.ok) return r.body.error ?? 'login failed';
  authState.authed = true;
  authState.root = false;
  authState.user = r.body.user;
  return null;
}

export async function logout(): Promise<void> {
  await api.post('/api/auth/logout', {});
  authState.authed = false;
  authState.root = false;
  authState.user = null;
}

/*
 * Any 401 anywhere flips the state AND navigates. The old version only flipped and left the
 * redirect to the router guard — which runs on navigation, so a session expiring while you sat on
 * a page produced a wall of dead panels that stayed until you happened to click something. The
 * navigation carries `next`, so signing back in lands where the session died, not on the dashboard.
 */
onUnauthorized(() => {
  if (!authState.checked) return;
  const wasAuthed = authState.authed;
  authState.authed = false;
  authState.user = null;
  authState.root = false;
  const route = router.currentRoute.value;
  if (wasAuthed && route.name !== 'login') {
    void router.replace({
      name: 'login',
      query: route.fullPath !== '/' ? { next: route.fullPath } : {},
    });
  }
});
