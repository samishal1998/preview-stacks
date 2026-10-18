import { createRouter, createWebHistory } from 'vue-router';

export const router = createRouter({
  history: createWebHistory(),
  scrollBehavior: (_to, _from, saved) => saved ?? { top: 0 },
  routes: [
    { path: '/', name: 'overview', component: () => import('./views/OverviewView.vue') },
    { path: '/deployments', name: 'deployments', component: () => import('./views/DeploymentsView.vue') },
    // A share link opens this. Public: the link's token authenticates its calls, so it renders outside the sign-in gate.
    { path: '/deployments/:id/public-logs-view', name: 'public-logs', component: () => import('./views/PublicLogsView.vue'), props: true, meta: { public: true } },
    { path: '/deployments/:id/:tab?', name: 'deployment', component: () => import('./views/DeploymentView.vue'), props: true },
    { path: '/jobs', name: 'jobs', component: () => import('./views/JobsView.vue') },
    { path: '/jobs/:id', name: 'job', component: () => import('./views/JobView.vue'), props: true },
    { path: '/specs', name: 'specs', component: () => import('./views/SpecsView.vue') },
    { path: '/specs/:name', name: 'spec', component: () => import('./views/SpecView.vue'), props: true },
    { path: '/submit/:id?', name: 'submit', component: () => import('./views/SubmitView.vue'), props: true },
    { path: '/routing', name: 'routing', component: () => import('./views/RoutingView.vue') },
    { path: '/swarm', name: 'swarm', component: () => import('./views/SwarmView.vue') },
    { path: '/control', name: 'control', component: () => import('./views/ControlView.vue') },
    { path: '/registries', name: 'registries', component: () => import('./views/RegistriesView.vue') },
    { path: '/notifiers', name: 'notifiers', component: () => import('./views/NotifiersView.vue') },
    { path: '/users', name: 'users', component: () => import('./views/UsersView.vue') },
    { path: '/sso', name: 'sso', component: () => import('./views/SsoView.vue') },
    { path: '/variables', name: 'variables', component: () => import('./views/VariablesView.vue') },
    { path: '/config', name: 'config', component: () => import('./views/ConfigView.vue') },
    { path: '/settings', name: 'settings', component: () => import('./views/SettingsView.vue') },
    { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('./views/NotFoundView.vue') },
  ],
});
