<script setup lang="ts">
import { computed, ref } from 'vue';
import { HAlertDialog, HAlert, HBadge, HCard, HDataTable, HEmptyState, HSkeleton, type MenuAction, type TableColumn, type TableRow } from '@hearth-ui/vue';
import { controlApi, why, type ControlRuntime } from '../../api-control';
import { ago, containerTone } from '../../format';
import { mem } from '../../lib/control';
import { byLabel } from '../../lib/labels';
import { toast } from '../../state';

const props = defineProps<{ runtime: ControlRuntime | null; loaded: boolean; error: string; canRestart: boolean }>();
const emit = defineEmits<{ reload: [] }>();

const columns: TableColumn[] = [
  { key: 'service', label: 'Service' },
  { key: 'image', label: 'Image', truncate: true, hideBelow: 760 },
  { key: 'state', label: 'State' },
  { key: 'restarts', label: 'Restarts' },
  { key: 'mem', label: 'Memory limit', hideBelow: 560 },
  { key: 'started', label: 'Started', hideBelow: 640 },
];
const list = computed(() => props.runtime?.containers ?? []);
const named = computed(() => byLabel(list.value, (c) => c.name, (c) => c.id));
const rows = computed<TableRow[]>(() =>
  [...named.value].map(([rid, c]) => ({
    id: rid,
    service: c.service || c.name,
    image: c.image,
    state: c.state,
    restarts: c.restartCount,
    mem: mem(c.memLimitBytes),
    started: ago(c.startedAt),
  })),
);
const find = (rowId: string) => named.value.get(rowId);

// pstack cannot restart itself: the server refuses its own container by name.
const rowActions = (row: TableRow): MenuAction[] => {
  const c = find(row.id);
  if (!props.canRestart || !c?.service || c.service === 'pstack') return [];
  return [{ id: 'restart', label: 'Restart', icon: 'arrow' }];
};

const confirm = ref({ open: false, service: '' });
const busy = ref(false);
function onAction(e: { id: string; action: string }): void {
  const c = find(e.id);
  if (c?.service && e.action === 'restart') confirm.value = { open: true, service: c.service };
}
async function restart(): Promise<void> {
  const service = confirm.value.service;
  busy.value = true;
  const r = await controlApi.restart(service);
  busy.value = false;
  confirm.value.open = false;
  if (!r.ok) {
    toast(why(r, `Restart ${service}`), 'danger');
    return;
  }
  toast(`Restarting ${r.body.container}.`, 'success');
  emit('reload');
}
</script>

<template>
  <HCard title="Containers">
    <HAlert v-if="error" tone="danger" :title="error" />
    <HSkeleton v-else-if="!loaded" variant="rectangle" />
    <HAlert v-else-if="runtime && !runtime.reachable" tone="warning" title="Docker is not answering." description="Nothing here is known." />
    <HEmptyState v-else-if="!rows.length" title="None listed" icon="server" />
    <HDataTable v-else label="Control containers" :rows="rows" :columns="columns" :get-row-actions="rowActions" @row-action="onAction">
      <template #cell="{ row, column, value }">
        <template v-if="column.key === 'service'">
          {{ value }}
          <HBadge v-if="value === 'pstack'" tone="info" label="This API" size="sm" />
          <HBadge v-if="value === 'pstack'" tone="neutral" label="Restart from the host" size="sm" />
        </template>
        <HBadge
          v-else-if="column.key === 'state'"
          :tone="containerTone(String(value), find(String(row.id))?.health ?? null)"
          :label="find(String(row.id))?.health ? `${value} · ${find(String(row.id))?.health}` : String(value)"
        />
        <template v-else-if="column.key === 'restarts'">
          <HBadge :tone="Number(value) > 0 ? 'warning' : 'neutral'" :label="String(value)" />
          <HBadge v-if="find(String(row.id))?.oomKilled" tone="danger" label="OOM" />
        </template>
        <template v-else>{{ value }}</template>
      </template>
    </HDataTable>
    <HAlertDialog
      v-model:open="confirm.open"
      :title="`Restart ${confirm.service}?`"
      confirm-label="Restart"
      :busy="busy"
      @confirm="restart"
    />
  </HCard>
</template>
