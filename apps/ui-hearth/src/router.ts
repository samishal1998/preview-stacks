import { createRouter, createWebHistory } from 'vue-router';

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'overview', component: () => import('./views/OverviewView.vue') },
    { path: '/deployments', name: 'deployments', component: () => import('./views/DeploymentsView.vue') },
    { path: '/deployments/:id/:tab?', name: 'deployment', component: () => import('./views/DeploymentView.vue'), props: true },
    { path: '/jobs', name: 'jobs', component: () => import('./views/JobsView.vue') },
    { path: '/jobs/:id', name: 'job', component: () => import('./views/JobView.vue'), props: true },
    { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('./views/NotFoundView.vue') },
  ],
});
