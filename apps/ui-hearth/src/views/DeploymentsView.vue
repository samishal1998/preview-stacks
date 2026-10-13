<script setup lang="ts">
/**
 * Every deployment, filterable, sortable and selectable, with the lifecycle actions on each row and
 * on the selection. One request per deployment — there is no bulk API, and each is its own job.
 */
import { computed, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlertDialog,
  HBadge,
  HButton,
  HChipGroup,
  HDataTable,
  HFilterBar,
  HInput,
  HList,
  HListItem,
  HPageHeader,
  HStack,
  HText,
  HToolbar,
  type MenuAction,
  type TableColumn,
  type TableRow,
  type TableSort,
} from '@hearth-ui/vue';
import type { DeploymentRow } from '@samyx/preview-stacks-client';
import { api } from '../api';
import { BULK_LABEL, skipReason, type BulkAction } from '../bulk';
import { ago } from '../format';
import { data, refresh, toast } from '../state';

const router = useRouter();

const q = ref('');
const kinds = ref<string[]>([]);
const states = ref<string[]>([]);
const selected = ref<string[]>([]);
const sort = ref<TableSort>({ key: 'id', direction: 'ascending' });
const page = ref(1);

const stateOf = (d: DeploymentRow) => (d.asleep ? 'asleep' : d.busy ? 'busy' : d.running ? 'running' : 'idle');

const rows = computed(() => {
  const needle = q.value.trim().toLowerCase();
  const list = data.deployments.filter((d) => {
    if (kinds.value.length && !kinds.value.includes(d.kind)) return false;
    if (states.value.length && !states.value.includes(stateOf(d))) return false;
    if (!needle) return true;
    return d.id.toLowerCase().includes(needle) || (d.stack ?? '').toLowerCase().includes(needle) || (d.specName ?? '').toLowerCase().includes(needle);
  });
  const dir = sort.value.direction === 'ascending' ? 1 : -1;
  const key = sort.value.key;
  return [...list].sort((a, b) => {
    const av = key === 'state' ? stateOf(a) : key === 'updated' ? (a.updatedAt ?? 0) : key === 'kind' ? a.kind : key === 'stack' ? (a.stack ?? '') : a.id;
    const bv = key === 'state' ? stateOf(b) : key === 'updated' ? (b.updatedAt ?? 0) : key === 'kind' ? b.kind : key === 'stack' ? (b.stack ?? '') : b.id;
    return av < bv ? -dir : av > bv ? dir : 0;
  });
});

const columns: TableColumn[] = [
  { key: 'id', label: 'ID', sortable: true },
  { key: 'kind', label: 'Kind', sortable: true, hideBelow: 520 },
  { key: 'stack', label: 'Stack', sortable: true, truncate: true, hideBelow: 700 },
  { key: 'state', label: 'State', sortable: true },
  { key: 'updated', label: 'Updated', sortable: true, align: 'end', hideBelow: 860 },
];
const tableRows = computed<TableRow[]>(() =>
  rows.value.map((d) => ({ id: d.id, kind: d.kind, stack: d.stack ?? 'needs variables', state: stateOf(d), updated: ago(d.updatedAt) })),
);

const rowActions = (row: TableRow): MenuAction[] => {
  const d = data.deployments.find((x) => x.id === row.id);
  if (!d) return [];
  const a = (id: BulkAction, label: string, extra: Partial<MenuAction> = {}): MenuAction => ({
    id,
    label,
    disabled: !!skipReason(d, id),
    ...extra,
  });
  return [
    { id: 'open', label: 'Open', icon: 'launch' },
    a('up', 'Deploy', { separatorBefore: true }),
    a('verify', 'Verify'),
    a('sleep', 'Sleep'),
    a('wake', 'Wake'),
    a('down', 'Tear down', { danger: true, separatorBefore: true }),
    a('down-forget', 'Tear down and forget', { danger: true }),
  ];
};

const kindOptions = computed(() =>
  (['isolated', 'shared'] as const).map((k) => ({ value: k, label: k, count: data.deployments.filter((d) => d.kind === k).length })),
);
const stateOptions = computed(() =>
  ['running', 'busy', 'asleep', 'idle'].map((s) => ({ value: s, label: s, count: data.deployments.filter((d) => stateOf(d) === s).length })),
);
const filtering = computed(() => kinds.value.length + states.value.length + (q.value ? 1 : 0));
function clearFilters(): void {
  q.value = '';
  kinds.value = [];
  states.value = [];
}

// ── acting ──────────────────────────────────────────────────────────────────────────────────────
const run = reactive({ busy: false, results: [] as Array<{ id: string; job?: string; error?: string; skipped?: string }>, label: '' });
const confirm = reactive({ open: false, action: 'down' as BulkAction, ids: [] as string[] });

async function send(ids: string[], action: BulkAction): Promise<void> {
  run.busy = true;
  run.label = BULK_LABEL[action];
  run.results = [];
  for (const id of ids) {
    const d = data.deployments.find((x) => x.id === id);
    const why = d ? skipReason(d, action) : 'gone';
    if (why) {
      run.results.push({ id, skipped: why });
      continue;
    }
    const verb = action === 'down-forget' ? 'down' : action;
    const r = await api.act(id, verb, action === 'down-forget' ? { forget: true } : {});
    if (r.status === 202 && r.body.job) run.results.push({ id, job: r.body.job.id });
    else run.results.push({ id, error: r.body.error ?? `HTTP ${r.status}` });
  }
  run.busy = false;
  const sent = run.results.filter((x) => x.job).length;
  const bad = run.results.filter((x) => x.error).length;
  toast(`${run.label}: ${sent} started`, bad ? 'danger' : 'success', bad ? `${bad} refused` : undefined);
  selected.value = [];
  void refresh();
}

function request(ids: string[], action: BulkAction): void {
  if (action === 'down' || action === 'down-forget') {
    const sendable = ids.filter((id) => {
      const d = data.deployments.find((x) => x.id === id);
      return d && !skipReason(d, action);
    });
    confirm.action = action;
    confirm.ids = ids;
    confirm.open = sendable.length > 0;
    if (!sendable.length) toast('Nothing to tear down here', 'warning', 'Shared stacks are torn down from their own page.');
    return;
  }
  void send(ids, action);
}

function onRowAction(e: { id: string; action: string }): void {
  if (e.action === 'open') void router.push(`/deployments/${encodeURIComponent(e.id)}`);
  else request([e.id], e.action as BulkAction);
}

const bulkActions: BulkAction[] = ['up', 'verify', 'sleep', 'wake', 'down', 'down-forget'];
const confirmCount = computed(
  () =>
    confirm.ids.filter((id) => {
      const d = data.deployments.find((x) => x.id === id);
      return d && !skipReason(d, confirm.action);
    }).length,
);
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Deployments" :description="`${data.deployments.length} submitted on this host.`" actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" @click="refresh" />
      </template>
    </HPageHeader>

    <HFilterBar label="Filters" :active-count="filtering" @clear="clearFilters">
      <HInput v-model="q" label="Search" hide-label type="search" placeholder="Search by id, stack or spec" leading-icon="search" clearable />
      <HChipGroup v-model="kinds" label="Kind" :options="kindOptions" />
      <HChipGroup v-model="states" label="State" :options="stateOptions" />
    </HFilterBar>

    <HToolbar v-if="selected.length" label="Selection">
      <HText weight="bold">{{ selected.length }} selected</HText>
      <HButton
        v-for="a in bulkActions"
        :key="a"
        size="compact"
        :variant="a.startsWith('down') ? 'danger' : 'secondary'"
        :label="BULK_LABEL[a]"
        :loading="run.busy && run.label === BULK_LABEL[a]"
        :disabled="run.busy"
        @click="request(selected, a)"
      />
      <HButton size="compact" variant="ghost" label="Clear" @click="selected = []" />
    </HToolbar>

    <HDataTable
      v-model:selected="selected"
      v-model:page="page"
      label="Deployments"
      :rows="tableRows"
      :columns="columns"
      :loading="!data.loaded"
      :sort="sort"
      :page-size="15"
      :get-row-actions="rowActions"
      row-activatable
      selectable
      empty-text="Nothing matches. Clear the filters, or submit a spec."
      @sort-change="sort = $event"
      @row-action="onRowAction"
      @row-activate="router.push(`/deployments/${encodeURIComponent(String($event.id))}`)"
    >
      <template #cell="{ column, value }">
        <HBadge
          v-if="column.key === 'state'"
          :label="String(value)"
          dot
          :tone="value === 'running' ? 'success' : value === 'busy' ? 'info' : value === 'asleep' ? 'neutral' : 'neutral'"
        />
        <HBadge v-else-if="column.key === 'kind'" :label="String(value)" :tone="value === 'shared' ? 'warning' : 'neutral'" variant="outline" />
        <template v-else>{{ value }}</template>
      </template>
    </HDataTable>

    <HList v-if="run.results.length" label="Last bulk run" divided>
      <HListItem
        v-for="r in run.results"
        :key="r.id"
        :title="r.id"
        :description="r.job ? 'job started' : r.error ?? `skipped: ${r.skipped}`"
        :tone="r.error ? 'danger' : r.job ? 'success' : 'neutral'"
        :badge="r.job ? 'started' : r.error ? 'refused' : 'skipped'"
        :href="r.job ? `/jobs/${encodeURIComponent(r.job)}` : undefined"
      />
    </HList>

    <HAlertDialog
      v-model:open="confirm.open"
      danger
      :title="`${BULK_LABEL[confirm.action]} ${confirmCount} deployment${confirmCount === 1 ? '' : 's'}?`"
      :description="confirm.action === 'down-forget' ? 'Containers and resources are torn down, and each deployment is forgotten if the teardown is clean.' : 'Containers and resources are torn down. Each is checked for leftovers.'"
      :confirm-label="BULK_LABEL[confirm.action]"
      @confirm="send(confirm.ids, confirm.action)"
    />
  </HStack>
</template>
