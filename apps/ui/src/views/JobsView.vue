<script setup lang="ts">
/** Job history. Polled by the shell already, so this view only filters what is in memory. */
import { computed, ref } from 'vue';
import { state } from '../composables/useControlPlane';
import { actionLabel, took } from '../composables/useFormat';
import StateBadge from '../components/StateBadge.vue';
import SkeletonList from '../components/SkeletonList.vue';
import RelativeTime from '../components/RelativeTime.vue';
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
        <div class="sub">Most recent first, capped at the last 50.</div>
      </div>
    </div>

    <section class="panel">
      <div class="phead">
        <div class="field" style="flex: 1; min-width: 180px">
          <label for="jq" class="mute" style="font-size: var(--t-xs)">Search — press /</label>
          <input id="jq" v-model="q" data-search type="search" placeholder="stack or action" spellcheck="false" />
        </div>
        <div class="field">
          <label for="js" class="mute" style="font-size: var(--t-xs)">State</label>
          <select id="js" v-model="only">
            <option value="all">all</option>
            <option value="running">running</option>
            <option value="ok">ok</option>
            <option value="leaked">leaked</option>
            <option value="failed">failed</option>
          </select>
        </div>
      </div>

      <p class="hint" style="margin: 0 0 var(--s3)">
        Held in memory. A server restart clears the history — a job record is the transcript of an
        attempt, not the truth about what exists. That truth lives in docker and in each axis's own
        probe.
      </p>

      <p v-if="state.jobsError" class="s-failed">{{ state.jobsError }}</p>
      <SkeletonList v-else-if="!state.jobsLoaded" :rows="5" />

      <table v-else class="cards">
        <thead>
          <tr>
            <th>state</th>
            <th>action</th>
            <th>stack</th>
            <th>started</th>
            <th>took</th>
            <th>steps</th>
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
