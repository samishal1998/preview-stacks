<script setup lang="ts">
/**
 * The frame: theme island, sign-in gate, dashboard shell, command palette and toaster.
 *
 * Everything below the frame is a view that reads `state.ts`; none of them owns navigation,
 * authentication or persistence beyond what Hearth's contract leaves to the application.
 */
import { computed, onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import {
  HAuthPage,
  HButton,
  HCommandPalette,
  HConnectionState,
  HDashboardShell,
  HPopover,
  HSelect,
  HStack,
  HSkeleton,
  HTheme,
  HThemeSwitcher,
  HToaster,
  type CommandItem,
  type NavItem,
} from '@hearth-ui/vue';
import { api } from './api';
import { auth, data, dismissToast, prefs, refresh, signOut, start, toasts } from './state';

const route = useRoute();
const router = useRouter();

const loginError = ref('');
const loggingIn = ref(false);
const paletteOpen = ref(false);

const items = computed<NavItem[]>(() => [
  { id: 'overview', label: 'Overview', icon: 'home' },
  { id: 'deployments', label: 'Deployments', icon: 'apps', badge: data.deployments.length || undefined },
  { id: 'jobs', label: 'Jobs', icon: 'clock', badge: data.jobs.filter((j) => j.state === 'running' || j.state === 'queued').length || undefined },
]);
const active = computed(() => {
  const n = String(route.name ?? '');
  return n === 'deployment' ? 'deployments' : n === 'job' ? 'jobs' : n;
});
const pageTitle = computed(() => items.value.find((i) => i.id === active.value)?.label ?? 'pstack');

function go(id: string): void {
  void router.push(id === 'overview' ? '/' : `/${id}`);
}

const commands = computed<CommandItem[]>(() => [
  { id: 'go:overview', label: 'Overview', group: 'Go to', icon: 'home' },
  { id: 'go:deployments', label: 'Deployments', group: 'Go to', icon: 'apps' },
  { id: 'go:jobs', label: 'Jobs', group: 'Go to', icon: 'clock' },
  { id: 'refresh', label: 'Refresh now', group: 'Actions', icon: 'arrow' },
  ...data.deployments.map((d) => ({
    id: `dep:${d.id}`,
    label: d.id,
    description: d.stack ?? 'needs variables',
    group: 'Deployments',
    icon: 'server',
    keywords: [d.kind, d.stack ?? ''],
  })),
]);

function onCommand(id: string): void {
  paletteOpen.value = false;
  if (id === 'refresh') void refresh();
  else if (id.startsWith('go:')) go(id.slice(3));
  else if (id.startsWith('dep:')) void router.push(`/deployments/${encodeURIComponent(id.slice(4))}`);
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

const palettes = ['sunset', 'ocean', 'forest', 'dusk', 'rose'].map((v) => ({ value: v, label: v[0]!.toUpperCase() + v.slice(1) }));
const densities = [
  { value: 'comfortable', label: 'Comfortable' },
  { value: 'compact', label: 'Compact' },
];

onMounted(start);
</script>

<template>
  <HTheme :theme="prefs.theme" :mode="prefs.mode" :density="prefs.density">
    <HStack v-if="!auth.checked" style="padding: 2rem" aria-busy="true">
      <HSkeleton variant="text" />
    </HStack>

    <HAuthPage
      v-else-if="!auth.authed"
      brand="pstack"
      title="Sign in"
      eyebrow="Preview stacks"
      description="The control plane for this host's previews."
      story-eyebrow="One host"
      story-title="Every preview."
      story-accent="One console."
      story-description="Deploy, verify, sleep and tear down the stacks on this host, and see what each job did."
      story-footer="pstack"
      note="Sessions are per browser."
      :scenery="false"
      :loading="loggingIn"
      :error="loginError || (data.connection === 'failed' ? data.error : '')"
      @submit="signIn"
    />

    <HDashboardShell
      v-else
      brand="pstack"
      :items="items"
      :active="active"
      :page-title="pageTitle"
      :username="auth.username"
      user-role="signed in"
      :show-workspace="false"
      navigation-label="Console"
      footer-note="pstack"
      inner-scroll
      @navigate="go"
      @logout="signOut"
    >
      <template #header-actions>
        <HConnectionState
          v-if="data.connection !== 'connected'"
          :state="data.connection"
          label="Server"
          :description="data.error"
          @retry="refresh"
        />
        <HButton variant="ghost" size="compact" icon="search" label="Search" @click="paletteOpen = true" />
        <HPopover label="Appearance" icon="settings" icon-only variant="ghost" panel-label="Appearance" placement="bottom">
          <HStack direction="column" :gap="3">
            <HThemeSwitcher v-model="prefs.mode" />
            <HSelect v-model="prefs.theme" label="Palette" :options="palettes" />
            <HSelect v-model="prefs.density" label="Density" :options="densities" />
          </HStack>
        </HPopover>
      </template>

      <RouterView />
    </HDashboardShell>

    <HCommandPalette v-model:open="paletteOpen" :items="commands" shortcut placeholder="Go to, or find a deployment…" @select="onCommand" />
    <HToaster :items="toasts" @dismiss="dismissToast" />
  </HTheme>
</template>
