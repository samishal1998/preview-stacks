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
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path
            d="M4 7.5 12 3l8 4.5v9L12 21l-8-4.5v-9Z"
            stroke="currentColor"
            stroke-width="1.6"
            stroke-linejoin="round"
          />
          <path d="M12 12v9M4 7.5 12 12l8-4.5" stroke="currentColor" stroke-width="1.6" />
        </svg>
        pstack
      </RouterLink>

      <nav aria-label="Sections">
        <RouterLink to="/" class="navlink" title="g h">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <path d="M4 12 12 4l8 8v8H4v-8Z" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round" />
          </svg>
          <span>Dashboard</span>
        </RouterLink>

        <RouterLink to="/deployments" class="navlink" title="g d">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <rect x="3" y="4" width="18" height="6" rx="2" stroke="currentColor" stroke-width="1.7" />
            <rect x="3" y="14" width="18" height="6" rx="2" stroke="currentColor" stroke-width="1.7" />
          </svg>
          <span>Deployments</span>
          <span class="count">{{ state.deployments.length }}</span>
        </RouterLink>

        <RouterLink to="/jobs" class="navlink" title="g j">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <circle cx="12" cy="12" r="8" stroke="currentColor" stroke-width="1.7" />
            <path d="M12 8v4l3 2" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" />
          </svg>
          <span>Jobs</span>
          <span class="count">{{ runningJobs ? `${runningJobs}▸` : state.jobs.length }}</span>
        </RouterLink>

        <RouterLink to="/submit" class="navlink" title="g n">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <path d="M12 5v14M5 12h14" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" />
          </svg>
          <span>Submit</span>
        </RouterLink>

        <RouterLink to="/settings" class="navlink" title="g s">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <circle cx="12" cy="12" r="3" stroke="currentColor" stroke-width="1.7" />
            <path
              d="M12 3v2m0 14v2M3 12h2m14 0h2M5.6 5.6l1.4 1.4m10 10 1.4 1.4m0-12.8-1.4 1.4m-10 10-1.4 1.4"
              stroke="currentColor"
              stroke-width="1.7"
              stroke-linecap="round"
            />
          </svg>
          <span>Settings</span>
        </RouterLink>
      </nav>

      <div class="foot">
        <div v-if="tokenMissing">
          <RouterLink to="/settings" class="badge warn">token required</RouterLink>
        </div>
        <div v-else-if="state.health?.authEnforced === false" class="mute">
          auth not enforced — loopback-only
        </div>
        <div v-if="state.health">v{{ state.health.version }}</div>
        <div v-if="state.health" class="mono">{{ state.health.dataDir }}</div>
        <div v-if="state.healthError" class="s-failed">API unreachable</div>
        <div><button class="ghost sm" @click="sheetOpen = true">? shortcuts</button></div>
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
