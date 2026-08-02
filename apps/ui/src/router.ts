/**
 * Routes.
 *
 * History mode, not hash mode: nginx serves this app with `try_files … /index.html`, so a deep
 * link like `/deployments/pr-7/danger` renders instead of 404ing. (The API host does the same
 * thing for the basic UI — every non-`/api/` path serves the document.)
 *
 * Views are lazily imported so the dashboard — the page an operator lands on when something is
 * wrong — is not waiting on the submit editor's bundle.
 */

import { nextTick } from 'vue';
import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router';

const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: () => import('./views/LoginView.vue') },
  { path: '/', name: 'dashboard', component: () => import('./views/DashboardView.vue') },
  { path: '/deployments', name: 'deployments', component: () => import('./views/DeploymentsView.vue') },
  {
    path: '/deployments/:id',
    component: () => import('./views/DeploymentDetailView.vue'),
    props: true,
    children: [
      { path: '', name: 'deployment', redirect: (to) => `/deployments/${to.params.id}/overview` },
      { path: 'overview', name: 'd.overview', component: () => import('./views/tabs/OverviewTab.vue') },
      { path: 'config', name: 'd.config', component: () => import('./views/tabs/ConfigTab.vue') },
      { path: 'runtime', name: 'd.runtime', component: () => import('./views/tabs/RuntimeTab.vue') },
      { path: 'axes', name: 'd.axes', component: () => import('./views/tabs/AxesTab.vue') },
      { path: 'requires', name: 'd.requires', component: () => import('./views/tabs/RequiresTab.vue') },
      { path: 'logs', name: 'd.logs', component: () => import('./views/tabs/LogsTab.vue') },
      { path: 'terminal', name: 'd.terminal', component: () => import('./views/tabs/TerminalTab.vue') },
      { path: 'danger', name: 'd.danger', component: () => import('./views/tabs/DangerTab.vue') },
    ],
  },
  { path: '/routing', name: 'routing', component: () => import('./views/RoutingView.vue') },
  { path: '/registries', name: 'registries', component: () => import('./views/RegistriesView.vue') },
  { path: '/notifiers', name: 'notifiers', component: () => import('./views/NotifiersView.vue') },
  { path: '/specs', name: 'specs', component: () => import('./views/SpecsView.vue') },
  { path: '/specs/:name', name: 'spec', component: () => import('./views/SpecDetailView.vue'), props: true },
  { path: '/submit/:id?', name: 'submit', component: () => import('./views/SubmitView.vue'), props: true },
  { path: '/jobs', name: 'jobs', component: () => import('./views/JobsView.vue') },
  { path: '/jobs/:jobId', name: 'job', component: () => import('./views/JobDetailView.vue'), props: true },
  { path: '/users', name: 'users', component: () => import('./views/UsersView.vue') },
  { path: '/settings', name: 'settings', component: () => import('./views/SettingsView.vue') },
  // A path that matches nothing renders a message, never a blank screen.
  { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('./views/NotFoundView.vue') },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: (_to, _from, saved) => saved ?? { top: 0 },
});

/**
 * The auth guard. Waits for the startup check (routing on unknown state would flash the login page
 * at every signed-in user), then keeps unauthenticated visitors on /login — carrying the intended
 * destination so signing in lands where they were going, not on the dashboard.
 */
router.beforeEach(async (to) => {
  const { authState, checkAuth } = await import('./composables/useAuth');
  if (!authState.checked) await checkAuth();
  if (to.name === 'login') {
    return authState.authed ? { path: '/' } : true;
  }
  if (!authState.authed) {
    return { name: 'login', query: to.fullPath !== '/' ? { next: to.fullPath } : {} };
  }
  return true;
});

/*
 * ── ROUTE TRANSITIONS ────────────────────────────────────────────────────────────────────────────
 *
 * The Vue `<Transition mode="out-in">` in App.vue fades the old view out and THEN the new one in, so
 * every navigation spends a full duration showing neither — a blink that gets more obvious the
 * faster the app gets. The View Transitions API snapshots both and cross-fades them simultaneously,
 * and it snapshots the WHOLE page, so a heading that stays put across two routes visibly stays put
 * instead of blinking with everything else.
 *
 * It is a progressive enhancement and stays one: where the API is missing, `data-vt` is never set,
 * App.vue keeps its CSS transition, and nothing about navigation changes. The two never both run.
 */
const supportsViewTransitions =
  typeof document !== 'undefined' && typeof document.startViewTransition === 'function';

if (supportsViewTransitions) document.documentElement.dataset.vt = 'on';

export { supportsViewTransitions };

if (supportsViewTransitions) {
  let commitDone: (() => void) | null = null;

  router.beforeResolve(
    () =>
      new Promise<void>((resolveNavigation) => {
        document.startViewTransition(() => {
          // Let the navigation commit, then HOLD the transition open until Vue has painted the new
          // view — returning immediately would snapshot the new state before it existed.
          resolveNavigation();
          return new Promise<void>((done) => {
            commitDone = done;
            /*
             * The safety net, and it is not paranoia. A view transition that is never released
             * leaves the browser showing a frozen SNAPSHOT of the page: no clicks, no scroll,
             * nothing. On a control plane someone opened because production is on fire, a UI that
             * can wedge itself over an animation is not a trade worth making — so the transition
             * always ends, on its own, whatever else went wrong.
             */
            setTimeout(() => {
              commitDone?.();
              commitDone = null;
            }, 400);
          });
        });
      }),
  );

  router.afterEach(async () => {
    await nextTick();
    commitDone?.();
    commitDone = null;
  });
}
