<script setup lang="ts">
/**
 * The first slice of an alternative console: sign in, Deployments, Jobs — read-only, on Hearth.
 *
 * It is a separate app from apps/ui, not a theme of it: the two share the API (through the client
 * package's types) and nothing else, so either can change without the other. Everything the
 * advanced UI does beyond this — lifecycle actions, logs, routing, settings — is still there.
 */
import { computed, onMounted, ref } from 'vue';
import { HAuthPage, HBadge, HCard, HDashboardShell, HPageHeader, HEmptyState } from '@hearth-ui/vue';
import type { DeploymentRow, Job } from '@samyx/preview-stacks-client';
import { api } from './api';

const checked = ref(false);
const authed = ref(false);
const username = ref('');
const loginError = ref('');
const loggingIn = ref(false);

const view = ref<'deployments' | 'jobs'>('deployments');
const deployments = ref<DeploymentRow[]>([]);
const jobs = ref<Job[]>([]);
const loadError = ref('');

const items = [
  { id: 'deployments', label: 'Deployments', icon: 'apps' },
  { id: 'jobs', label: 'Jobs', icon: 'activity' },
];
const title = computed(() => (view.value === 'jobs' ? 'Jobs' : 'Deployments'));

async function refresh(): Promise<void> {
  loadError.value = '';
  const [d, j] = await Promise.all([api.deployments(), api.jobs()]);
  if (d.status === 401 || j.status === 401) {
    authed.value = false;
    return;
  }
  if (!d.ok || !j.ok) loadError.value = d.body.error ?? j.body.error ?? 'could not load';
  deployments.value = d.body.deployments ?? [];
  jobs.value = j.body.jobs ?? [];
}

async function start(): Promise<void> {
  const me = await api.me();
  if (me.status === 0) {
    loginError.value = me.body.error ?? '';
  } else if (me.ok) {
    authed.value = true;
    username.value = me.body.user?.username ?? (me.body.root ? 'root' : '');
    await refresh();
  }
  checked.value = true;
}

async function signIn(c: { username: string; password: string }): Promise<void> {
  loggingIn.value = true;
  loginError.value = '';
  const r = await api.login(c.username, c.password);
  loggingIn.value = false;
  if (!r.ok) {
    loginError.value = r.body.error ?? 'sign-in failed';
    return;
  }
  await start();
}

async function signOut(): Promise<void> {
  await api.logout();
  authed.value = false;
  deployments.value = [];
  jobs.value = [];
}

const tone = (s: string) =>
  s === 'ok' || s === 'running' ? 'success' : s === 'failed' || s === 'leaked' ? 'danger' : 'neutral';

onMounted(start);
</script>

<template>
  <HAuthPage
    v-if="checked && !authed"
    brand="pstack"
    title="Sign in"
    description="Preview stacks for this host."
    :loading="loggingIn"
    :error="loginError"
    @submit="signIn"
  />

  <HDashboardShell
    v-else-if="authed"
    brand="pstack"
    :items="items"
    :active="view"
    :page-title="title"
    :username="username"
    :show-workspace="false"
    navigation-label="pstack"
    user-role="signed in"
    footer-note="Preview stacks"
    @navigate="view = $event as 'deployments' | 'jobs'"
    @logout="signOut"
  >
    <HPageHeader :title="title" />
    <p v-if="loadError">{{ loadError }}</p>

    <template v-if="view === 'deployments'">
      <HEmptyState v-if="!deployments.length" title="Nothing submitted yet" />
      <HCard v-for="d in deployments" :key="d.id" :title="d.id" :description="d.stack ?? 'needs variables'">
        <HBadge :label="d.kind" />
        <HBadge v-if="d.asleep" label="asleep" />
        <HBadge v-else-if="d.busy" label="busy" tone="info" />
        <HBadge v-else-if="d.running" label="running" tone="success" />
      </HCard>
    </template>

    <template v-else>
      <HEmptyState v-if="!jobs.length" title="No jobs recorded" />
      <HCard v-for="j in jobs" :key="j.id" :title="`${j.action} · ${j.stack}`" :description="j.deployment ?? 'control plane'">
        <HBadge :label="j.state" :tone="tone(j.state)" />
      </HCard>
    </template>
  </HDashboardShell>
</template>
