<script setup lang="ts">
/**
 * The frame: theme island, sign-in gate, dashboard shell, command palette and toaster.
 *
 * Everything below the frame is a view that reads `state.ts`; none of them owns navigation,
 * authentication or persistence beyond what Hearth's contract leaves to the application.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import {
  HAuthPage,
  HButton,
  HCommandPalette,
  HConnectionState,
  HDashboardShell,
  HDescriptionList,
  HDialog,
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
import SignIn from './components/SignIn.vue';
import { ssoApi } from './api-sso';
import { densities, palettes } from './lib/settings';
import { isTyping, keyStep, SHORTCUTS } from './lib/shortcuts';
import { showGrafana } from './lib/grafana';
import { safeNext } from './lib/sso';
import { can, sentence, type Role } from './lib/users';
import { auth, data, dismissToast, grafana, prefs, refresh, signInWithToken, signOut, start, toasts } from './state';

// About 60 % of the library's 10/16/20px corners. Overlays are native dialogs and popovers inside the island, so they inherit these.
const RADIUS = { '--h-radius-control': '6px', '--h-radius-card': '10px', '--h-radius-panel': '14px' };

const route = useRoute();
const router = useRouter();

const loginError = ref('');
const loggingIn = ref(false);
const paletteOpen = ref(false);

/** `min` is courtesy: a page the role cannot use is not listed. The server still decides. */
const all = computed<Array<NavItem & { min?: Role }>>(() => [
  { id: 'overview', label: 'Overview', icon: 'home' },
  { id: 'deployments', label: 'Deployments', icon: 'apps', badge: data.deployments.length || undefined },
  { id: 'jobs', label: 'Jobs', icon: 'clock', badge: data.jobs.filter((j) => j.state === 'running' || j.state === 'queued').length || undefined },
  { id: 'specs', label: 'Specs', icon: 'code' },
  { id: 'routing', label: 'Routing', icon: 'globe' },
  { id: 'swarm', label: 'Swarm', icon: 'server' },
  { id: 'control', label: 'Control', icon: 'layout', min: 'maintainer' },
  { id: 'registries', label: 'Registries', icon: 'lock' },
  { id: 'notifiers', label: 'Notifiers', icon: 'info' },
  { id: 'users', label: 'Users', icon: 'providers' },
  { id: 'sso', label: 'Single sign-on', icon: 'launch', min: 'maintainer' },
  { id: 'variables', label: 'Variables', icon: 'star' },
  { id: 'config', label: 'Config', icon: 'copy', min: 'admin' },
  { id: 'settings', label: 'Settings', icon: 'settings' },
]);
const items = computed<NavItem[]>(() => {
  const rows = all.value.filter((i) => !i.min || can(i.min, auth)).map(({ min: _min, ...i }) => i);
  // Grafana is its own site; developer and up (it reads unredacted lines).
  return showGrafana(grafana.value, can('developer', auth), auth.viaToken) ? [...rows, { id: 'grafana', label: 'Grafana', icon: 'launch' }] : rows;
});
const active = computed(() => {
  const n = String(route.name ?? '');
  return n === 'deployment' || n === 'submit' ? 'deployments' : n === 'job' ? 'jobs' : n === 'spec' ? 'specs' : n;
});
const pageTitle = computed(() => items.value.find((i) => i.id === active.value)?.label ?? 'pstack');

function go(id: string): void {
  if (id === 'grafana') {
    window.open(grafana.value, '_blank', 'noopener');
    return;
  }
  void router.push(id === 'overview' ? '/' : `/${id}`);
}

const commands = computed<CommandItem[]>(() => [
  { id: 'go:overview', label: 'Overview', group: 'Go to', icon: 'home' },
  { id: 'go:deployments', label: 'Deployments', group: 'Go to', icon: 'apps' },
  ...items.value.filter((i) => !['overview', 'deployments'].includes(i.id)).map((i) => ({ id: `go:${i.id}`, label: i.label, group: 'Go to', icon: i.icon })),
  { id: 'go:submit', label: 'Submit a spec', group: 'Actions', icon: 'plus' },
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
  // Back to where the person was going: /login carries it as ?next=, anywhere else the URL already is it.
  if (auth.authed && route.path === '/login') leave(route.query.next);
}

async function useToken(token: string): Promise<void> {
  loggingIn.value = true;
  loginError.value = (await signInWithToken(token)) ?? '';
  loggingIn.value = false;
  if (auth.authed && route.path === '/login') leave(route.query.next);
}

/** An /api/ target (Grafana's sign-in) is a server route: full navigation, not a view. */
function leave(next: unknown): void {
  const to = safeNext(next);
  if (to.startsWith('/api/')) window.location.assign(to);
  else void router.replace(to);
}

watch(
  () => auth.authed,
  (on) => {
    if (!on) {
      grafana.value = '';
    } else {
      void ssoApi.health().then((h) => (grafana.value = h.ok ? (h.body.grafana ?? '') : ''));
      if (route.path === '/login') leave(route.query.next);
    }
  },
  { immediate: true },
);


const helpOpen = ref(false);
let pendingAt = 0;
function onKey(e: KeyboardEvent): void {
  if (!auth.authed || paletteOpen.value || helpOpen.value) return;
  const s = keyStep(pendingAt, { key: e.key, typing: isTyping(e.target), mod: e.metaKey || e.ctrlKey || e.altKey }, Date.now());
  pendingAt = s.pendingAt;
  if (s.go) void router.push(s.go);
  else if (s.help) helpOpen.value = true;
  else return;
  e.preventDefault();
}
onMounted(() => {
  void start();
  window.addEventListener('keydown', onKey);
});
onBeforeUnmount(() => window.removeEventListener('keydown', onKey));
</script>

<template>
  <HTheme :theme="prefs.theme" :mode="prefs.mode" :density="prefs.density" :tokens="RADIUS">
    <!-- A public page (a share link) renders without the sign-in gate or the shell. -->
    <RouterView v-if="route.meta.public" />

    <HStack v-else-if="!auth.checked" style="padding: 2rem" aria-busy="true">
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
    >
      <template #form>
        <SignIn :loading="loggingIn" :error="loginError || (data.connection === 'failed' ? data.error : '')" @submit="signIn" @token="useToken" />
      </template>
    </HAuthPage>

    <HDashboardShell
      v-else
      brand="pstack"
      :items="items"
      :active="active"
      :page-title="pageTitle"
      :username="auth.username"
      :user-role="auth.role ? sentence(auth.role) : auth.root ? 'Host token' : 'Signed in'"
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
        <HButton variant="ghost" size="compact" icon="info" label="Shortcuts" @click="helpOpen = true" />
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
    <HDialog :open="helpOpen" title="Shortcuts" @close="helpOpen = false">
      <HDescriptionList variant="inline" :items="SHORTCUTS.map((x) => ({ key: x.keys, label: x.keys, value: x.what }))" />
    </HDialog>
    <HToaster :items="toasts" @dismiss="dismissToast" />
  </HTheme>
</template>

<style>
/* The browser's 8px body margin insets the app and adds a second scrollbar. */
body {
  margin: 0;
}
/* HLogViewer's action row does not wrap: at 320 "Copy visible logs" runs past the card. */
.h-log-actions {
  flex-wrap: wrap;
}
/* HResourceDetail's tab buttons sit 3px apart; the list scrolls, so no wrap. */
.h-tabs .h-tablist[role="tablist"] {
  gap: 8px;
}
/* An empty state inside a card: 56px padding and a title bigger than the card's own made "No routes" outrank "Routing". */
.h-card .h-empty {
  padding: 28px 16px;
}
.h-card .h-empty h2 {
  font-size: 1rem;
}
/* The sticky action cell paints --h-raised, a lighter band on the card surface it sits in. Not in
   card layout: there the cell has no fill, and this one paints a band across the card. */
.sticky-actions:not(.cards) td.h-table-actions.h-table-actions {
  background: var(--h-surface);
}
/* HCard's #footer slot sits flush under the body (its 20px gap is for the custom element only). */
.h-card > .h-card-body + * {
  margin-top: 20px;
}
/* HAlert's title is inline, so slot text runs straight into it ("Replacing pr-12345Anything…") and a
   slot button shares its line, pushing the text below the icon. */
.h-alert-content > strong[part="title"] {
  display: block;
}
/* Overlay headers top-align the title with the Close button: one line sits off-centre (5px high in a
   dialog, 8-12px low beside the 44px mobile button). A title block as tall as the button centres one
   line and still starts a wrapped title at the top. */
.h-dialog-head > :first-child,
.h-mobile-overlay.h-mobile .h-overlay-header:has(> .h-button) > :first-child {
  display: flex;
  flex-direction: column;
  justify-content: center;
  min-height: var(--h-control-height);
}
.h-dialog .h-dialog-head.h-overlay-header > .h-button {
  margin-top: 0;
}
/* HDialog's #footer slot sits flush under the body (its 24px gap is for the custom element only). */
.h-dialog .h-overlay-footer:not(:empty) {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 24px;
}
</style>
