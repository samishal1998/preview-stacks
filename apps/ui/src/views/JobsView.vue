<script setup lang="ts">
/** Job history. Polled by the shell already, so this view only filters what is in memory. */
import { computed, ref } from 'vue';
import { state } from '../composables/useControlPlane';
import { actionLabel, took } from '../composables/useFormat';
import StateBadge from '../components/StateBadge.vue';
import SkeletonList from '../components/SkeletonList.vue';
import RelativeTime from '../components/RelativeTime.vue';
import SelectMenu from '../components/SelectMenu.vue';
import InfoHint from '../components/InfoHint.vue';
import type { JobState } from '../api/types';

const q = ref('');
const only = ref<'all' | JobState>('all');

const rows = computed(() => {
  const needle = q.value.trim().toLowerCase();
  return state.jobs.filter((j) => {
    if (only.value !== 'all' && j.state !== only.value) return false;
    if (!needle) return true;
    return j.stack.toLowerCase().includes(needle) || j.action.includes(needle);
  });
});
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Jobs</h1>
        <div class="sub">
          Most recent first, capped at the last 50
          <InfoHint label="why old jobs disappear">
            The history is held in memory and a server restart clears it. A job record is the
            transcript of an attempt, not the truth about what exists — that truth lives in the
            containers themselves and in each axis's own probe.
          </InfoHint>
        </div>
      </div>
    </div>

    <section class="panel">
      <!-- Same one-row toolbar as Deployments — this page had kept the old stacked-label layout,
           and two list pages with two different toolbars reads as two different products. -->
      <div class="phead">
        <div class="searchbox">
          <svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true">
            <circle cx="11" cy="11" r="7" fill="none" stroke="currentColor" stroke-width="2" />
            <path d="M16.5 16.5 21 21" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
          </svg>
          <input
            id="jq"
            v-model="q"
            data-search
            type="search"
            aria-label="Search jobs"
            placeholder="Search by stack or action"
            spellcheck="false"
          />
          <kbd v-if="!q">/</kbd>
        </div>
        <SelectMenu
          id="js"
          v-model="only"
          label="Filter by state"
          :options="[
            { value: 'all', label: 'All states' },
            { value: 'running', label: 'Running' },
            { value: 'ok', label: 'Ok' },
            { value: 'leaked', label: 'Leaked' },
            { value: 'failed', label: 'Failed' },
          ]"
        />
      </div>

      <p v-if="state.jobsError" class="s-failed">{{ state.jobsError }}</p>
      <SkeletonList v-else-if="!state.jobsLoaded" :rows="5" />

      <table v-else class="cards">
        <thead>
          <tr>
            <th>State</th>
            <th>Action</th>
            <th>Stack</th>
            <th>Started</th>
            <th>Took</th>
            <th>Steps</th>
          </tr>
        </thead>
        <tbody class="stagger">
          <tr v-for="(j, i) in rows" :key="j.id" :style="{ '--i': i }">
            <td data-label="state">
              <RouterLink :to="`/jobs/${encodeURIComponent(j.id)}`">
                <StateBadge :state="j.state" />
              </RouterLink>
            </td>
            <td data-label="action">{{ actionLabel(j.action) }}</td>
            <td class="name" data-label="stack">
              <RouterLink :to="`/jobs/${encodeURIComponent(j.id)}`">{{ j.stack }}</RouterLink>
            </td>
            <td class="dim nowrap" data-label="started"><RelativeTime :at="j.startedAt" /></td>
            <td class="dim nowrap" data-label="took">{{ took(j.startedAt, j.endedAt) }}</td>
            <td class="dim" data-label="steps">{{ j.outcome ? j.outcome.steps.length : '—' }}</td>
          </tr>
          <tr v-if="!rows.length">
            <td colspan="6" class="mute">
              <template v-if="state.jobs.length">Nothing matches this filter.</template>
              <template v-else>No jobs recorded.</template>
            </td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>
</template>
