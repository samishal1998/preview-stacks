<script setup lang="ts">
/** Job history, newest first. `?deployment=<id>` narrows it: the link a deployment page uses. */
import { computed, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import {
  HAlert,
  HBadge,
  HButton,
  HChip,
  HChipGroup,
  HDataTable,
  HFilterBar,
  HInput,
  HPageHeader,
  HStack,
  HText,
  type TableColumn,
  type TableRow,
} from '@hearth-ui/vue';
import { actionLabel, ago, jobTone, sentence, took } from '../format';
import { jobTarget, supersededBy, waitReason, waitText } from '../lib/jobs';
import { useNow } from '../lib/now';
import { useSlashFocus } from '../lib/use-slash-focus';
import { data, refresh } from '../state';

const route = useRoute();
const router = useRouter();
const now = useNow();

const q = ref('');
const states = ref<string[]>(typeof route.query.state === 'string' ? [route.query.state] : []);
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
  { key: 'steps', label: 'Steps', align: 'end', hideBelow: 820 },
];

/** Under a queued or superseded badge: why it is not moving, or what replaced it. */
function note(j: (typeof jobs.value)[number]): { text: string; to?: string } | null {
  if (j.state === 'queued') return { text: waitText(waitReason(j, data.jobs)) };
  if (j.state !== 'superseded') return null;
  const by = supersededBy(j, data.jobs);
  return by ? { text: `Replaced by a newer ${actionLabel(by.action).toLowerCase()}.`, to: `/jobs/${encodeURIComponent(by.id)}` } : { text: 'Never ran.' };
}

const rows = computed<TableRow[]>(() =>
  jobs.value.map((j) => {
    const n = note(j);
    return {
    id: j.id,
    state: j.state,
    action: actionLabel(j.action),
    stack: j.stack,
    target: jobTarget(j) ?? '',
    note: n?.text ?? '',
    noteTo: n?.to ?? '',
    started: ago(j.startedAt, now.value),
    took: took(j.startedAt, j.endedAt, now.value),
    steps: j.outcome ? j.outcome.steps.length : '—',
    };
  }),
);
const stateOptions = computed(() =>
  ['queued', 'running', 'ok', 'failed', 'leaked', 'cancelled', 'superseded'].map((s) => ({ value: s, label: s, count: data.jobs.filter((j) => j.state === s).length })),
);
const filtering = computed(() => states.value.length + (q.value ? 1 : 0) + (deployment.value ? 1 : 0));
function clear(): void {
  q.value = '';
  states.value = [];
  if (deployment.value) void router.replace('/jobs');
}

// "/" focuses the search box, as in the first UI.
const search = useSlashFocus();
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Jobs" description="Newest first, the last 50. A restart clears them." actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" @click="refresh" />
      </template>
    </HPageHeader>

    <HAlert v-if="!data.loaded && data.error" tone="danger" title="Could not load jobs" :description="data.error" />

    <template v-else>
      <HFilterBar label="Filters" :active-count="filtering" @clear="clear">
        <div ref="search">
          <HInput v-model="q" label="Search" hide-label type="search" placeholder="Search by stack or action. Press /" leading-icon="search" clearable />
        </div>
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
        :empty-text="data.jobs.length ? 'Nothing matches. Clear the filters.' : 'No jobs recorded.'"
        @row-activate="router.push(`/jobs/${encodeURIComponent(String($event.id))}`)"
      >
        <template #cell="{ row, column, value }">
          <template v-if="column.key === 'state'">
            <HBadge dot :label="String(value)" :tone="jobTone(String(value))" :title="sentence(String(value))" />
            <HText v-if="row.note" tone="muted" size="sm" as="div">
              <template v-if="row.noteTo">
                <RouterLink :to="String(row.noteTo)">{{ row.note }}</RouterLink>
              </template>
              <template v-else>{{ row.note }}</template>
            </HText>
          </template>
          <RouterLink v-else-if="column.key === 'stack' && row.target" :to="String(row.target)">{{ value }}</RouterLink>
          <template v-else>{{ value }}</template>
        </template>
      </HDataTable>
    </template>
  </HStack>
</template>
