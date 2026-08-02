<script setup lang="ts">
/**
 * The shell: rail, view, toasts, shortcut sheet.
 *
 * The rail's counts come from the shared control-plane state, which is polled here rather than per
 * view — so the numbers are right even on a deep link straight into a job, and a background tab
 * stops polling entirely (see `usePolling`).
 */
import { computed, watch } from 'vue';
import ToastHost from './components/ToastHost.vue';
import ShortcutSheet from './components/ShortcutSheet.vue';
import CommandPalette from './components/CommandPalette.vue';
import { supportsViewTransitions } from './router';
import { useShortcuts } from './composables/useShortcuts';
import { usePolling } from './composables/usePolling';
import { loadDeployments, loadHealth, loadJobs, state } from './composables/useControlPlane';
import { settings } from './composables/useSettings';
import { authState, logout } from './composables/useAuth';
import { useRouter } from 'vue-router';
import InfoHint from './components/InfoHint.vue';
// Named imports, so the bundler ships only these glyphs — not the whole set.
import {
  BellRing,
  Boxes,
  ClockFading,
  FileCode2,
  KeyRound,
  LayoutDashboard,
  Package,
  Plus,
  Settings2,
  Users,
  Waypoints,
} from 'lucide-vue-next';

const { sheetOpen } = useShortcuts();

usePolling(() => {
  // Not before sign-in: every poll would 401, flashing failure state over the login page.
  if (!authState.authed) return;
  void loadDeployments();
  void loadJobs();
}, 7000);

// The poll gate above skips ticks while signed out, so the tick that WOULD have populated the rail
// ran early and did nothing. Load immediately when auth arrives — otherwise the first data appears
// up to a full poll interval after login, which reads as a broken dashboard.
watch(
  () => authState.authed,
  (authed) => {
    if (authed) {
      void loadDeployments();
      void loadJobs();
      void loadHealth();
    }
  },
);

const router = useRouter();
async function signOut(): Promise<void> {
  await logout();
  void router.push('/login');
}

// Health barely changes; fetch it once and again only if it failed.
void loadHealth();

const runningJobs = computed(() => state.jobs.filter((j) => j.state === 'running').length);

// The old "token required" warning is gone with 0.10.0: nobody sees this rail without already
// being authenticated (session, personal token, or the machine token), so the warning could only
// ever appear when it was already false.
void settings;
</script>

<template>
  <a class="skip" href="#main">Skip to content</a>

  <div class="shell" :class="{ 'no-rail': !authState.authed }">
    <!-- Signed out there is nothing to navigate to — every link would bounce back to /login — so
         the rail is dead chrome and the login card gets the whole viewport. -->
    <aside v-if="authState.authed" class="rail">
      <RouterLink to="/" class="brand">
        <Package :size="20" aria-hidden="true" />
        pstack
      </RouterLink>

      <nav aria-label="Sections">
        <RouterLink to="/" class="navlink" title="g h">
          <LayoutDashboard :size="17" aria-hidden="true" />
          <span>Dashboard</span>
        </RouterLink>

        <RouterLink to="/deployments" class="navlink" title="g d">
          <Boxes :size="17" aria-hidden="true" />
          <span>Deployments</span>
          <span class="count">{{ state.deployments.length }}</span>
        </RouterLink>

        <RouterLink to="/specs" class="navlink">
          <FileCode2 :size="17" aria-hidden="true" />
          <span>Specs</span>
        </RouterLink>

        <RouterLink to="/routing" class="navlink">
          <Waypoints :size="17" aria-hidden="true" />
          <span>Routing</span>
        </RouterLink>

        <RouterLink to="/registries" class="navlink">
          <KeyRound :size="17" aria-hidden="true" />
          <span>Registries</span>
        </RouterLink>

        <RouterLink to="/notifiers" class="navlink">
          <BellRing :size="17" aria-hidden="true" />
          <span>Notifiers</span>
        </RouterLink>

        <RouterLink to="/jobs" class="navlink" title="g j">
          <ClockFading :size="17" aria-hidden="true" />
          <span>Jobs</span>
          <span class="count">{{ runningJobs ? `${runningJobs}\u25B8` : state.jobs.length }}</span>
        </RouterLink>

        <RouterLink to="/submit" class="navlink" title="g n">
          <Plus :size="17" aria-hidden="true" />
          <span>Submit</span>
        </RouterLink>

        <RouterLink to="/users" class="navlink">
          <Users :size="17" aria-hidden="true" />
          <span>Users</span>
        </RouterLink>

        <RouterLink to="/settings" class="navlink" title="g s">
          <Settings2 :size="17" aria-hidden="true" />
          <span>Settings</span>
        </RouterLink>
      </nav>

      <div class="foot">
        <div v-if="state.healthError" class="s-failed">Can't reach the server</div>
        <div v-else-if="state.health" class="row" style="gap: 2px">
          <span>v{{ state.health.version }}</span>
          <InfoHint label="server details" side="top">
            Data is stored in <code>{{ state.health.dataDir }}</code> on the host.
            <template v-if="state.health.authEnforced === false">
              This server accepts connections from this machine only, so it does not ask for a
              token.
            </template>
          </InfoHint>
        </div>
        <div v-if="authState.user" class="row" style="gap: 6px">
          <span>{{ authState.user.username }}</span>
          <button class="ghost sm" @click="signOut">Sign out</button>
        </div>
        <div v-else-if="authState.root && authState.checked" class="mute">token access</div>
        <div><button class="ghost sm" @click="sheetOpen = true">Shortcuts</button></div>
      </div>
    </aside>

    <main id="main" class="view">
      <RouterView v-slot="{ Component }">
        <!--
          Exactly ONE of these runs. `:css="false"` was not enough: `mode="out-in"` still
          orchestrates the removal, and Vue unmounting a node the browser had already swapped for a
          view-transition snapshot threw `Cannot read properties of null (reading 'parentNode')` on
          every navigation. Where the browser animates, Vue does not manage the swap at all.
        -->
        <component :is="Component" v-if="supportsViewTransitions" />
        <Transition v-else name="view" mode="out-in">
          <component :is="Component" />
        </Transition>
      </RouterView>
    </main>
  </div>

  <ToastHost />
  <CommandPalette />
  <ShortcutSheet :open="sheetOpen" @close="sheetOpen = false" />
</template>
