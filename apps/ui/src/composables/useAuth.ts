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

export const authState = reactive({
  /** The startup check has completed — routing decisions before this would flicker. */
  checked: false,
  authed: false,
  root: false,
  user: null as { id: number; username: string; role: string } | null,
  /** From /api/health: whether any account exists — decides "sign in" vs "bootstrap first". */
  hasUsers: null as boolean | null,
});

export async function checkAuth(): Promise<void> {
  const health = await api.get<{ hasUsers?: boolean }>('/api/health');
  if (health.ok) authState.hasUsers = health.body.hasUsers ?? null;

  const me = await api.getAuthed<{ root: boolean; user?: { id: number; username: string; role: string } }>(
    '/api/auth/me',
  );
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
  const r = await api.post<{ user: { id: number; username: string; role: string } }>(
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

// Any 401 anywhere flips the state; the router guard turns that into a redirect. Registered at
// module scope so it exists before the first request fires.
onUnauthorized(() => {
  if (authState.checked) {
    authState.authed = false;
    authState.user = null;
    authState.root = false;
  }
});
