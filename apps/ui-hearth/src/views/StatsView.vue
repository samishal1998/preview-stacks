<script setup lang="ts">
/**
 * Node pressure: per node, disk, memory and load against the pressure settings, an hour of load, the
 * pressure the server reports and the last prune; then what each stack uses. Polls every 10 s.
 *
 * Pressure is the server's verdict (`node.pressure`), never recomputed here. A stale node shows its
 * readings as unknown. The upgrade advice shows only when no node has ever reported (NP-R13).
 */
import { computed, ref } from 'vue';
import {
  HAlert, HBadge, HButton, HDataTable, HEmptyState, HHeading, HPageHeader, HProgress, HSkeleton, HSparkline, HStack, HStatCard, HText,
  type TableRow, type Tone,
} from '@hearth-ui/vue';
import type { NodeStats, SettingKey, Stats } from '@samyx/preview-stacks-client';
import { statsApi } from '../api-stats';
import { settingsApi } from '../api-settings';
import { poll } from '../api-control';
import { deploymentFor, diskUsedPct, fmtBytes, loadPerCore, memFreePct, neverReported, pressureLabel, sortStacks } from '../lib/stats';
import { useCardColumns } from '../lib/use-card-columns';
import { ago } from '../format';
import { data } from '../state';

const stats = ref<Stats | null>(null);
const limits = ref<Partial<Record<SettingKey, number>>>({});
const error = ref('');
const refreshing = ref(false);

let inflight = false;
async function load(manual = false): Promise<void> {
  if (inflight) return;
  inflight = true;
  if (manual) refreshing.value = true;
  const [s, t] = await Promise.all([statsApi.read(), settingsApi.read()]);
  inflight = false;
  refreshing.value = false;
  if (t.ok) limits.value = Object.fromEntries(t.body.settings.filter((r) => typeof r.value === 'number').map((r) => [r.key, r.value]));
  if (!s.ok) {
    if (stats.value) return; // a failed background poll keeps the last good data
    error.value = `Could not read stats: ${s.body.error ?? `HTTP ${s.status}`}`;
    return;
  }
  error.value = '';
  stats.value = { ...s.body, nodes: s.body.nodes ?? [], stacks: s.body.stacks ?? [] };
}
poll(() => load(), 10_000);

const none = computed(() => !!stats.value && neverReported(stats.value));

const pct = (v: number) => `${Math.round(v)}%`;
const limit = (k: SettingKey, show: (v: number) => string) => {
  const v = limits.value[k];
  return v === undefined ? '' : v === 0 ? 'Limit off.' : `Limit ${show(v)}.`;
};

type Bar = { key: string; label: string; value: number | null; max: number; showValue: boolean; detail: string; tone: Tone };
function bars(n: NodeStats): Bar[] {
  const tone = (kind: string): Tone => (n.pressure.includes(kind) ? 'danger' : 'accent');
  const disk = diskUsedPct(n);
  const memFree = memFreePct(n);
  const load = loadPerCore(n);
  const cpuLimit = limits.value.pressure_cpu_load || 1;
  return [
    {
      key: 'disk', label: 'Disk used', value: disk, max: 100, showValue: true, tone: tone('disk'),
      detail: `${fmtBytes(n.diskFree)} free of ${fmtBytes(n.diskTotal)}. ${limit('pressure_disk_pct', pct)}`,
    },
    {
      key: 'memory', label: 'Memory used', value: memFree === null ? null : 100 - memFree, max: 100, showValue: true, tone: tone('memory'),
      detail: `${fmtBytes(n.memAvailable)} free of ${fmtBytes(n.memTotal)}. ${limit('pressure_mem_pct', (v) => `${v}% free`)}`,
    },
    {
      key: 'cpu', label: 'Load per core', value: load, max: Math.max(cpuLimit, load ?? 0),
      showValue: false, tone: tone('cpu'),
      detail: `${load === null ? '—' : load.toFixed(2)} on ${n.cores ?? '—'} cores. ${limit('pressure_cpu_load', String)}`,
    },
  ];
}

const state = (n: NodeStats): { value: string; tone: Tone } =>
  n.stale ? { value: 'Stale', tone: 'neutral' } : n.pressure.length ? { value: 'Pressure', tone: 'danger' } : { value: 'OK', tone: 'success' };
/** A stale node's `at` is null; its newest sample still says when it last reported. */
const lastSeen = (n: NodeStats) => n.at ?? n.history.at(-1)?.at ?? null;
const detail = (n: NodeStats) => [n.role, lastSeen(n) ? `reported ${ago(lastSeen(n))}` : 'never reported'].filter(Boolean).join(' · ');
const pruned = (n: NodeStats) => (n.prune ? `Pruned ${ago(n.prune.at)}, ${fmtBytes(n.prune.freedBytes)} freed (${n.prune.reason}).` : 'Not pruned yet.');

const columns = useCardColumns([
  { key: 'stack', label: 'Stack' },
  { key: 'mem', label: 'Memory', align: 'end' },
  { key: 'cpu', label: 'CPU', align: 'end' },
  { key: 'containers', label: 'Containers', align: 'end', hideBelow: 640 },
  { key: 'nodes', label: 'Nodes', hideBelow: 820 },
]);
const rows = computed<TableRow[]>(() =>
  sortStacks(stats.value?.stacks ?? []).map((s) => {
    const dep = deploymentFor(s.stack, data.deployments);
    return {
      id: s.stack,
      stack: s.stack,
      target: dep ? `/deployments/${encodeURIComponent(dep)}` : '',
      mem: fmtBytes(s.mem),
      cpu: `${s.cpuPct.toFixed(1)}%`,
      containers: String(s.containers),
      nodes: s.nodes.join(', ') || '—',
    };
  }),
);
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Stats" description="Node pressure and what each stack uses." actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" :loading="refreshing" @click="load(true)" />
      </template>
    </HPageHeader>

    <HAlert v-if="error" tone="danger" :description="error" />
    <HSkeleton v-else-if="!stats" variant="text" :lines="4" label="Loading stats" />
    <HEmptyState v-else-if="none" icon="health" title="No agent reports.">
      <HText>Run <code>pstack upgrade</code>.</HText>
    </HEmptyState>

    <template v-else>
      <HHeading :level="2" size="md">Nodes</HHeading>
      <div class="stats-nodes">
        <HStack v-for="n in stats.nodes" :key="n.node ?? n.hostname" direction="column" :gap="3">
          <HStatCard :label="n.hostname" :value="state(n).value" :tone="state(n).tone" icon="server" :detail="detail(n)" />
          <HStack v-if="n.pressure.length" direction="row" :gap="2" wrap>
            <HBadge v-for="k in n.pressure" :key="k" dot tone="danger" :label="pressureLabel(k)" />
          </HStack>
          <template v-for="b in bars(n)" :key="b.key">
            <HProgress v-if="b.value !== null" :label="b.label" :value="b.value" :max="b.max" :show-value="b.showValue" :tone="b.tone" :description="b.detail" />
            <HText v-else tone="muted" size="sm">{{ b.label }}: unknown.</HText>
          </template>
          <HText v-if="n.history.length" tone="muted" size="sm" aria-hidden="true">Load, last hour</HText>
          <HSparkline
            v-if="n.history.length"
            :values="n.history.map((h) => h.load5)"
            label="Load, last hour"
            :description="`${n.history.length} samples.`"
            tone="info"
          />
          <HText tone="muted" size="sm">{{ pruned(n) }}</HText>
        </HStack>
      </div>

      <HHeading :level="2" size="md">Stacks</HHeading>
      <HDataTable label="Stacks" :rows="rows" :columns="columns" :page-size="20" empty-text="No stack containers reported.">
        <template #cell="{ row, column, value }">
          <RouterLink v-if="column.key === 'stack' && row.target" :to="String(row.target)">{{ value }}</RouterLink>
          <template v-else>{{ value }}</template>
        </template>
      </HDataTable>
    </template>
  </HStack>
</template>

<style scoped>
.stats-nodes {
  display: grid;
  gap: 24px;
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 20rem), 1fr));
}
</style>
