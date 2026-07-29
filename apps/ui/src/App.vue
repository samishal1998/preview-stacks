<script setup lang="ts">
/**
 * The shell: rail, view, toasts, shortcut sheet.
 *
 * The rail's counts come from the shared control-plane state, which is polled here rather than per
 * view — so the numbers are right even on a deep link straight into a job, and a background tab
 * stops polling entirely (see `usePolling`).
 */
import { computed } from 'vue';
import ToastHost from './components/ToastHost.vue';
import ShortcutSheet from './components/ShortcutSheet.vue';
import { useShortcuts } from './composables/useShortcuts';
import { usePolling } from './composables/usePolling';
import { loadDeployments, loadHealth, loadJobs, state } from './composables/useControlPlane';
import { settings } from './composables/useSettings';
import InfoHint from './components/InfoHint.vue';
// Named imports, so the bundler ships only these glyphs — not the whole set.
import {
  Boxes,
  ClockFading,
  FileCode2,
  LayoutDashboard,
  Package,
  Plus,
  Settings2,
  Waypoints,
} from 'lucide-vue-next';

const { sheetOpen } = useShortcuts();

usePolling(() => {
  void loadDeployments();
  void loadJobs();
}, 7000);

// Health barely changes; fetch it once and again only if it failed.
void loadHealth();

const runningJobs = computed(() => state.jobs.filter((j) => j.state === 'running').length);

/**
 * The one health fact worth putting in permanent view. `authEnforced: false` is a deliberate
 * loopback-only mode — the server refuses to bind anything but 127.0.0.1 without a token — not a
 * misconfiguration, so it is stated rather than warned about. A token being REQUIRED and missing
 * is the other way round: every action will 401, so that one is a warning.
 */
const tokenMissing = computed(() => state.health?.authEnforced === true && !settings.token);
</script>

<template>
  <a class="skip" href="#main">Skip to content</a>

  <div class="shell">
    <aside class="rail">
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

        <RouterLink to="/jobs" class="navlink" title="g j">
          <ClockFading :size="17" aria-hidden="true" />
          <span>Jobs</span>
          <span class="count">{{ runningJobs ? `${runningJobs}\u25B8` : state.jobs.length }}</span>
        </RouterLink>

        <RouterLink to="/submit" class="navlink" title="g n">
          <Plus :size="17" aria-hidden="true" />
          <span>Submit</span>
        </RouterLink>

        <RouterLink to="/settings" class="navlink" title="g s">
          <Settings2 :size="17" aria-hidden="true" />
          <span>Settings</span>
        </RouterLink>
      </nav>

      <div class="foot">
        <div v-if="tokenMissing">
          <RouterLink to="/settings" class="badge warn">Token required</RouterLink>
        </div>
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
        <div><button class="ghost sm" @click="sheetOpen = true">Shortcuts</button></div>
      </div>
    </aside>

    <main id="main" class="view">
      <RouterView v-slot="{ Component }">
        <Transition name="view" mode="out-in">
          <component :is="Component" />
        </Transition>
      </RouterView>
    </main>
  </div>

  <ToastHost />
  <ShortcutSheet :open="sheetOpen" @close="sheetOpen = false" />
</template>
