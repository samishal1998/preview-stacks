<script setup lang="ts">
/**
 * The deployment list: searchable, filterable, and honest about what could not be resolved.
 *
 * Filtering is client-side because the API has no query parameters for it and the list is one
 * host's worth of previews — a few dozen rows, already in memory from the shell's poll.
 */
import { computed, ref } from 'vue';
import { sentence } from '../composables/useFormat';
import { loadDeployments, state, summary } from '../composables/useControlPlane';

import RunStateBadge from '../components/RunStateBadge.vue';
import SkeletonList from '../components/SkeletonList.vue';
import RelativeTime from '../components/RelativeTime.vue';
import SelectMenu from '../components/SelectMenu.vue';
import RefreshButton from '../components/RefreshButton.vue';

const q = ref('');
const kind = ref<'all' | 'isolated' | 'shared'>('all');
const onlyLive = ref(false);

const rows = computed(() => {
  const needle = q.value.trim().toLowerCase();
  return state.deployments.filter((d) => {
    if (kind.value !== 'all' && d.kind !== kind.value) return false;
    // "Live" means DEMONSTRABLY live. An unknown is not evidence of either state, so it is not
    // silently included here — the filter says what it filters.
    // Asleep counts as live: it is deliberately down and comes back on a request — not torn down.
    if (onlyLive.value && !(d.running === true || d.busy === true || d.asleep)) return false;
    if (!needle) return true;
    return (
      d.id.toLowerCase().includes(needle) ||
      (d.stack ?? '').toLowerCase().includes(needle) ||
      (d.specName ?? '').toLowerCase().includes(needle)
    );
  });
});

const unresolvedRows = computed(() => state.deployments.filter((d) => d.unresolved));
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Deployments</h1>
        <div class="sub">
          {{ summary.total }} submitted · {{ summary.running }} running · {{ summary.busy }} busy
        </div>
      </div>
      <span class="grow" />
      <RefreshButton :run="loadDeployments" />
      <RouterLink to="/submit" class="btn">+ Submit</RouterLink>
    </div>

    <section class="panel">
      <div class="phead">
        <!--
          One row of equal-height controls. Each used to sit under its own stacked label, which cost
          a line of vertical space per control, put "Search — press /" on screen as a permanent
          instruction, and left the two fields 8px out of vertical alignment because a `select` and
          an `input` do not have the same intrinsic height. A placeholder says the same thing in the
          place you are already looking, and the select's own options say what it filters.
        -->
        <div class="searchbox">
          <svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true">
            <circle cx="11" cy="11" r="7" fill="none" stroke="currentColor" stroke-width="2" />
            <path d="M16.5 16.5 21 21" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
          </svg>
          <input
            id="q"
            v-model="q"
            data-search
            type="search"
            aria-label="Search deployments"
            placeholder="Search by id, stack or spec"
            spellcheck="false"
          />
          <kbd v-if="!q">/</kbd>
        </div>
        <SelectMenu
          v-model="kind"
          label="Filter by kind"
          :options="[
            { value: 'all', label: 'All kinds' },
            { value: 'isolated', label: 'Isolated' },
            { value: 'shared', label: 'Shared' },
          ]"
        />
        <label class="check">
          <input v-model="onlyLive" type="checkbox" />
          Running or busy only
        </label>
      </div>

      <p v-if="state.deploymentsError" class="s-failed">{{ state.deploymentsError }}</p>
      <SkeletonList v-else-if="!state.deploymentsLoaded" :rows="4" tall />

      <template v-else>
        <table class="cards">
          <thead>
            <tr>
              <th>ID</th>
              <th>Kind</th>
              <th>Stack</th>
              <th>State</th>
              <th>Updated</th>
            </tr>
          </thead>
          <tbody class="stagger">
            <tr v-for="(d, i) in rows" :key="d.id" :style="{ '--i': i }">
              <td data-label="id">
                <RouterLink :to="`/deployments/${encodeURIComponent(d.id)}`">
                  {{ d.id }}
                </RouterLink>
                <div v-if="d.specName" class="mute" style="font-size: var(--t-xs)">
                  spec: {{ d.specName }}
                </div>
              </td>
              <td data-label="kind"><span class="badge" :class="d.kind">{{ sentence(d.kind) }}</span></td>
              <td class="name dim" data-label="stack">
                <span v-if="d.stack">{{ d.stack }}</span>
                <!--
                  No stack name means the spec could not be resolved with the variables this
                  listing had. It is not an error state for the deployment — it is a missing input.
                -->
                <span v-else class="badge warn" title="the spec could not be resolved without variables">
                  needs variables
                </span>
              </td>
              <td data-label="state"><RunStateBadge :busy="d.busy" :running="d.running" :asleep="d.asleep" /></td>
              <td class="dim nowrap" data-label="updated"><RelativeTime :at="d.updatedAt" /></td>
            </tr>
            <tr v-if="!rows.length">
              <td colspan="5" class="mute">
                <template v-if="state.deployments.length">
                  Nothing matches this filter.
                </template>
                <template v-else>
                  Nothing submitted yet — <RouterLink to="/submit">submit a spec</RouterLink> to
                  put a deployment in the registry.
                </template>
              </td>
            </tr>
          </tbody>
        </table>

        <!-- One message per unresolved row, verbatim: it names the missing variable. -->
        <div v-for="d in unresolvedRows" :key="`u-${d.id}`" class="banner warn">
          <b>{{ d.id }}</b> could not be resolved:
          <span class="mono">{{ d.unresolved }}</span>
          <p>
            This listing resolves every spec with the <em>same</em> (empty) request variables, so a
            spec with a <code>${VAR}</code> the server does not already hold will always appear
            here.
            <RouterLink :to="`/deployments/${encodeURIComponent(d.id)}/config`">
              Set its variables
            </RouterLink>
            and the detail view will resolve.
          </p>
        </div>
      </template>
    </section>
  </div>
</template>
