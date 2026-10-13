<script setup lang="ts">
/** Job history, newest first. `?deployment=<id>` narrows it — the link a deployment page uses. */
import { computed, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import {
  HBadge,
  HButton,
  HChip,
  HChipGroup,
  HDataTable,
  HFilterBar,
  HInput,
  HPageHeader,
  HStack,
  type TableColumn,
  type TableRow,
} from '@hearth-ui/vue';
import { ACTION_LABEL, ago, jobTone, took } from '../format';
import { data, refresh } from '../state';

const route = useRoute();
const router = useRouter();

const q = ref('');
const states = ref<string[]>([]);
const page = ref(1);
const deployment = computed(() => (typeof route.query.deployment === 'string' ? route.query.deployment : ''));

const jobs = computed(() => {
  const needle = q.value.trim().toLowerCase();
  return data.jobs.filter((j) => {
    if (deployment.value && j.deployment !== deployment.value) return false;
    if (states.value.length && !states.value.includes(j.state)) return false;
    return !needle || j.stack.toLowerCase().includes(needle) || j.action.includes(needle);
  });
});

const columns: TableColumn[] = [
  { key: 'state', label: 'State' },
  { key: 'action', label: 'Action' },
  { key: 'stack', label: 'Stack', truncate: true },
  { key: 'started', label: 'Started', hideBelow: 560 },
  { key: 'took', label: 'Took', align: 'end', hideBelow: 700 },
];
const rows = computed<TableRow[]>(() =>
  jobs.value.map((j) => ({
    id: j.id,
    state: j.state,
    action: ACTION_LABEL[j.action] ?? j.action,
    stack: j.stack,
    started: ago(j.startedAt),
    took: took(j.startedAt, j.endedAt),
  })),
);
const stateOptions = computed(() =>
  ['queued', 'running', 'ok', 'failed', 'leaked', 'cancelled', 'superseded'].map((s) => ({
    value: s,
    label: s,
    count: data.jobs.filter((j) => j.state === s).length,
  })),
);
const filtering = computed(() => states.value.length + (q.value ? 1 : 0) + (deployment.value ? 1 : 0));
function clear(): void {
  q.value = '';
  states.value = [];
  if (deployment.value) void router.replace('/jobs');
}
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Jobs" description="Most recent first, the last 50. A restart clears the history." actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" @click="refresh" />
      </template>
    </HPageHeader>

    <HFilterBar label="Filters" :active-count="filtering" @clear="clear">
      <HInput v-model="q" label="Search" hide-label type="search" placeholder="Search by stack or action" leading-icon="search" clearable />
      <HChipGroup v-model="states" label="State" :options="stateOptions" />
      <HChip v-if="deployment" :label="`Deployment ${deployment}`" removable @remove="router.replace('/jobs')" />
    </HFilterBar>

    <HDataTable
      v-model:page="page"
      label="Jobs"
      :rows="rows"
      :columns="columns"
      :loading="!data.loaded"
      :page-size="15"
      row-activatable
      empty-text="No jobs match."
      @row-activate="router.push(`/jobs/${encodeURIComponent(String($event.id))}`)"
    >
      <template #cell="{ column, value }">
        <HBadge v-if="column.key === 'state'" dot :label="String(value)" :tone="jobTone(String(value))" />
        <template v-else>{{ value }}</template>
      </template>
    </HDataTable>
  </HStack>
</template>
