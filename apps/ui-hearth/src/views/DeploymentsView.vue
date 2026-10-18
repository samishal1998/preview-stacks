<script setup lang="ts">
/**
 * Every deployment, filterable, sortable and selectable, with the lifecycle actions on each row and
 * on the selection. One request per deployment: there is no bulk API, and each is its own job.
 */
import { computed, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HChipGroup,
  HDataTable,
  HEmptyState,
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
import { api } from '../api';
import { BULK_LABEL, chosen, isTearDown, sendable, skipReason, type BulkAction } from '../bulk';
import { filterDeployments, runState, summarize } from '../lib/deployments';
import { problem } from '../lib/problem';
import { useNow } from '../lib/now';
import { useSlashFocus } from '../lib/use-slash-focus';
import { ago, runTone } from '../format';
import { can } from '../lib/users';
import { auth, data, refresh, toast } from '../state';

const router = useRouter();
const now = useNow();

const q = ref('');
const kinds = ref<string[]>([]);
const states = ref<string[]>([]);
/** Courtesy: lifecycle actions are developer's. The server still decides. */
const canAct = computed(() => can('developer', auth));
const selected = ref<string[]>([]);
const sort = ref<TableSort>({ key: 'id', direction: 'ascending' });
const page = ref(1);

const sum = computed(() => summarize(data.deployments));
const rows = computed(() => filterDeployments(data.deployments, { q: q.value, kinds: kinds.value, states: states.value }));
const picked = computed(() => chosen(rows.value, selected.value));
const sharedPicked = computed(() => picked.value.filter((d) => d.kind === 'shared').length);
const unresolved = computed(() => data.deployments.filter((d) => d.unresolved));

const columns: TableColumn[] = [
  { key: 'id', label: 'ID', sortable: true },
  { key: 'kind', label: 'Kind', sortable: true, hideBelow: 520 },
  { key: 'stack', label: 'Stack', sortable: true, truncate: true, hideBelow: 700 },
  { key: 'state', label: 'State', sortable: true },
  { key: 'updated', label: 'Updated', sortable: true, align: 'end', hideBelow: 860 },
];
const tableRows = computed<TableRow[]>(() =>
  rows.value.map((d) => ({
    id: d.id,
    spec: d.specName ?? '',
    kind: d.kind,
    stack: d.stack ?? '',
    state: runState(d),
    updated: d.updatedAt ?? 0, // the number sorts; the cell renders it
  })),
);

const rowActions = (row: TableRow): MenuAction[] => {
  const d = data.deployments.find((x) => x.id === row.id);
  if (!d) return [];
  const a = (id: BulkAction, extra: Partial<MenuAction> = {}): MenuAction => ({ id, label: BULK_LABEL[id], disabled: run.busy || !!skipReason(d, id), ...extra });
  return [
    { id: 'open', label: 'Open', icon: 'launch' },
    a('up', { separatorBefore: true }),
    a('verify'),
    a('sleep'),
    a('wake'),
    a('down', { danger: true, separatorBefore: true }),
    a('down-forget', { danger: true }),
  ];
};

const kindOptions = computed(() => (['isolated', 'shared'] as const).map((k) => ({ value: k, label: k, count: sum.value[k] })));
const stateOptions = computed(() =>
  (['running', 'busy', 'asleep', 'idle', 'unknown'] as const).map((s) => ({ value: s, label: s, count: data.deployments.filter((d) => runState(d) === s).length })),
);
const filtering = computed(() => kinds.value.length + states.value.length + (q.value ? 1 : 0));
function clearFilters(): void {
  q.value = '';
  kinds.value = [];
  states.value = [];
}

// ── acting ──────────────────────────────────────────────────────────────────────────────────────
type Result = { id: string; job?: { id: string; state: string }; error?: string; skipped?: string };
const run = reactive({ busy: false, action: '' as BulkAction | '', results: [] as Result[] });
/** The job's state now: the 202 only knew it as queued or running. */
const jobState = (j: { id: string; state: string }): string => data.jobs.find((x) => x.id === j.id)?.state ?? j.state;
const confirm = reactive({ open: false, action: 'down' as BulkAction, ids: [] as string[] });

async function send(ids: string[], action: BulkAction): Promise<void> {
  confirm.open = false;
  run.busy = true;
  run.action = action;
  run.results = [];
  for (const id of ids) {
    const d = data.deployments.find((x) => x.id === id);
    const why = d ? skipReason(d, action) : 'gone';
    if (why) {
      run.results.push({ id, skipped: why });
      continue;
    }
    const r = await api.act(id, action === 'down-forget' ? 'down' : action, action === 'down-forget' ? { forget: true } : {});
    if (r.status === 202 && r.body.job) run.results.push({ id, job: { id: r.body.job.id, state: r.body.job.state } });
    else run.results.push({ id, error: problem(r, BULK_LABEL[action].toLowerCase()) });
  }
  run.busy = false;
  const sent = run.results.filter((x) => x.job).length;
  const bad = run.results.filter((x) => x.error).length;
  toast(`${BULK_LABEL[action]}: ${sent} started`, bad ? 'danger' : 'success', bad ? `${bad} refused` : undefined);
  selected.value = selected.value.filter((id) => !ids.includes(id));
  void refresh();
}

const byIds = (ids: string[]) => data.deployments.filter((d) => ids.includes(d.id));
const confirmCount = computed(() => sendable(byIds(confirm.ids), confirm.action));

function request(ids: string[], action: BulkAction): void {
  if (!isTearDown(action)) return void send(ids, action);
  if (!sendable(byIds(ids), action)) {
    toast('Nothing to tear down here', 'warning', 'Shared stacks are torn down from their own page.');
    return;
  }
  Object.assign(confirm, { action, ids, open: true });
}

function onRowAction(e: { id: string; action: string }): void {
  if (e.action === 'open') void router.push(`/deployments/${encodeURIComponent(e.id)}`);
  else request([e.id], e.action as BulkAction);
}

const plain: BulkAction[] = ['up', 'verify', 'sleep', 'wake'];
const destructive: BulkAction[] = ['down', 'down-forget'];

// "/" focuses the search box, as in the first UI.
const search = useSlashFocus();
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Deployments" :description="`${sum.total} submitted · ${sum.running} running · ${sum.busy} busy`" actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" @click="refresh" />
        <HButton v-if="can('developer', auth)" icon="plus" label="Submit" @click="router.push('/submit')" />
      </template>
    </HPageHeader>

    <HAlert v-if="!data.loaded && data.error" tone="danger" title="Could not load deployments" :description="data.error" />

    <HEmptyState v-else-if="data.loaded && !data.deployments.length" title="Nothing submitted yet" icon="apps">
      <HButton v-if="can('developer', auth)" icon="plus" label="Submit a spec" @click="router.push('/submit')" />
    </HEmptyState>

    <template v-else>
      <HFilterBar label="Filters" :active-count="filtering" @clear="clearFilters">
        <div ref="search">
          <HInput v-model="q" label="Search" hide-label type="search" placeholder="Search deployments" leading-icon="search" clearable />
        </div>
        <HChipGroup v-model="kinds" label="Kind" :options="kindOptions" />
        <HChipGroup v-model="states" label="State" :options="stateOptions" />
      </HFilterBar>

      <HToolbar v-if="canAct && (picked.length || run.busy)" label="Selection">
        <HText weight="bold">{{ picked.length }} selected</HText>
        <HButton v-if="picked.length < rows.length" size="compact" variant="ghost" :label="`Select all ${rows.length}`" @click="selected = rows.map((d) => d.id)" />
        <HButton size="compact" variant="ghost" label="Clear" @click="selected = []" />
        <HText v-if="sharedPicked" tone="muted">{{ sharedPicked }} shared, skipped by tear down</HText>
        <HButton
          v-for="a in plain"
          :key="a"
          size="compact"
          variant="secondary"
          :label="BULK_LABEL[a]"
          :loading="run.busy && run.action === a"
          :disabled="run.busy || !sendable(picked, a)"
          @click="request(picked.map((d) => d.id), a)"
        />
        <HButton
          v-for="a in destructive"
          :key="a"
          size="compact"
          variant="danger"
          :label="BULK_LABEL[a]"
          :loading="run.busy && run.action === a"
          :disabled="run.busy || !sendable(picked, a)"
          @click="request(picked.map((d) => d.id), a)"
        />
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
        :get-row-actions="canAct ? rowActions : undefined"
        row-activatable
        :selectable="canAct"
        empty-text="Nothing matches. Clear the filters."
        @sort-change="sort = $event"
        @row-action="onRowAction"
        @row-activate="router.push(`/deployments/${encodeURIComponent(String($event.id))}`)"
      >
        <template #cell="{ row, column, value }">
          <template v-if="column.key === 'id'">
            {{ value }}
            <HText v-if="row.spec" tone="muted" size="sm"> · {{ row.spec }}</HText>
          </template>
          <HBadge v-else-if="column.key === 'state'" dot :label="String(value)" :tone="runTone(String(value))" />
          <HBadge v-else-if="column.key === 'kind'" :label="String(value)" :tone="value === 'shared' ? 'warning' : 'neutral'" variant="outline" />
          <HBadge v-else-if="column.key === 'stack' && !value" label="Needs variables" tone="warning" />
          <template v-else-if="column.key === 'updated'">{{ ago(Number(value) || null, now) }}</template>
          <template v-else>{{ value }}</template>
        </template>
      </HDataTable>

      <HAlert v-for="d in unresolved" :key="d.id" tone="warning" :title="`${d.id} could not be resolved`" :description="d.unresolved" compact>
        <HButton size="compact" variant="secondary" label="Set its variables" @click="router.push(`/deployments/${encodeURIComponent(d.id)}/config`)" />
      </HAlert>

      <HList v-if="run.results.length && run.action" :label="`${BULK_LABEL[run.action]} results`" divided>
        <HListItem
          v-for="r in run.results"
          :key="r.id"
          :title="r.id"
          :description="r.error ?? (r.job ? jobState(r.job) : `skipped: ${r.skipped}`)"
          :tone="r.error || (r.job && ['failed', 'leaked'].includes(jobState(r.job))) ? 'danger' : r.job ? 'success' : 'neutral'"
          :badge="r.job ? 'job' : r.error ? 'refused' : 'skipped'"
          :interactive="!!r.job"
          @activate="r.job && router.push(`/jobs/${encodeURIComponent(r.job.id)}`)"
        />
      </HList>
      <HButton v-if="run.results.length && !run.busy" size="compact" variant="ghost" label="Dismiss results" @click="run.results = []" />
    </template>

    <HAlertDialog
      v-model:open="confirm.open"
      danger
      :title="`${BULK_LABEL[confirm.action]} ${confirmCount} deployment${confirmCount === 1 ? '' : 's'}?`"
      :description="confirm.action === 'down-forget' ? 'Torn down, then forgotten if the teardown is clean.' : 'Torn down and checked for leftovers.'"
      :confirm-label="BULK_LABEL[confirm.action]"
      :busy="run.busy"
      @confirm="send(confirm.ids, confirm.action)"
    />
  </HStack>
</template>
