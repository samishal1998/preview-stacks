<script setup lang="ts">
/**
 * The detail shell: breadcrumb, tabs, and the resolve-failure banner that gates the rest.
 *
 * A deployment whose spec will not resolve is not a dead end — the fix (its variables) lives on
 * the Config tab, so this sends the operator there rather than leaving them in front of an error
 * with no control in reach. Everything else stays unavailable until it resolves, because the
 * server cannot name the stack without the variables, and every action needs the stack name.
 */
import { watch } from 'vue';
import { useRouter } from 'vue-router';
import { dep, isShared, openDeployment } from '../composables/useDeployment';
import ErrorNote from '../components/ErrorNote.vue';

const props = defineProps<{ id: string }>();
const router = useRouter();

const TABS = [
  { to: 'overview', label: 'Overview' },
  { to: 'config', label: 'Config & variables' },
  { to: 'axes', label: 'Axes' },
  { to: 'requires', label: 'Requires' },
  { to: 'logs', label: 'Logs' },
  { to: 'danger', label: 'Danger' },
] as const;

watch(() => props.id, (id) => openDeployment(id), { immediate: true });

// Land the operator on the editor when the spec will not resolve — but only from the tab that has
// nothing to show. Sending them away from Logs or Danger mid-read would be its own annoyance.
watch(
  () => dep.error,
  (err) => {
    if (err && router.currentRoute.value.name === 'd.overview') {
      void router.replace(`/deployments/${encodeURIComponent(props.id)}/config`);
    }
  },
);
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="mute" style="font-size: var(--t-sm)">
          <RouterLink to="/deployments">← deployments</RouterLink>
        </div>
        <h1 class="mono" style="font-size: var(--t-xl)">{{ id }}</h1>
        <div class="sub">
          <span v-if="dep.detail">
            stack <span class="mono">{{ dep.detail.stack }}</span>
          </span>
          <span v-else-if="dep.error">unresolved</span>
          <span v-else>loading…</span>
        </div>
      </div>
      <span class="grow" />
      <span v-if="dep.detail" class="badge" :class="dep.detail.kind">{{ dep.detail.kind }}</span>
      <span v-if="dep.detail?.busy === true" class="badge busy"><span class="dot pulse" />busy</span>
    </div>

    <nav class="tabs" aria-label="Deployment sections">
      <RouterLink
        v-for="t in TABS"
        :key="t.to"
        :to="`/deployments/${encodeURIComponent(id)}/${t.to}`"
        >{{ t.label }}</RouterLink
      >
    </nav>

    <ErrorNote v-if="dep.error" :text="dep.error" title="Could not resolve this deployment.">
    </ErrorNote>
    <p v-if="dep.error" class="hint">
      If that message names a variable, add it under <b>Config &amp; variables</b> and press
      <b>Apply &amp; reload</b>. Everything else on this page stays unavailable until the spec
      resolves — the server cannot tell us the stack name without it, and every action needs the
      stack name.
    </p>

    <div v-if="isShared" class="banner warn">
      <b>This is a <code>kind: shared</code> deployment.</b>
      <p>
        A host singleton that previews borrow — a database, a queue, a registry mirror. It declares
        no axes, and its <code>down</code> is refused without an explicit force, because
        <code>compose down -v</code> here destroys state every other deployment depends on.
      </p>
    </div>

    <RouterView v-slot="{ Component }">
      <Transition name="view" mode="out-in">
        <component :is="Component" />
      </Transition>
    </RouterView>
  </div>
</template>
