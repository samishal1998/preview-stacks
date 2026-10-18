<script setup lang="ts">
/** The swarm: nodes, node actions, stuck services, and what a new worker needs to join. */
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue';
import {
  HAlert, HAlertDialog, HBadge, HButton, HCodeBlock, HDataTable, HHeading, HList, HListItem, HPageHeader, HSelect, HStack, HText,
  type MenuAction, type TableColumn, type TableRow,
} from '@hearth-ui/vue';
import type { SwarmInfo } from '@samyx/preview-stacks-client';
import { swarmApi, type Signals } from '../api-swarm';
import { poll } from '../api-control';
import { byLabel } from '../lib/labels';
import { DISTROS, joinPath, joinProblem, removable, type JoinFormat } from '../lib/swarm';
import { ago } from '../format';
import { can } from '../lib/users';
import { auth, toast } from '../state';

/** Courtesy: node actions and the join token are maintainer's. The server still decides. */
const manage = computed(() => can('maintainer', auth));
const info = ref<SwarmInfo | null>(null);
const named = computed(() => byLabel(info.value?.nodes ?? [], (n) => n.hostname || n.id.slice(0, 12), (n) => n.id.slice(0, 12)));
const signals = ref<Signals | null>(null);
const error = ref('');
const loaded = ref(false);
const refreshing = ref(false);

let inflight = false;
async function load(manual = false): Promise<void> {
  if (inflight) return;
  inflight = true;
  if (manual) refreshing.value = true;
  const [i, s] = await Promise.all([swarmApi.info(), swarmApi.signals()]);
  inflight = false;
  refreshing.value = false;
  loaded.value = true;
  if (!i.ok) {
    if (info.value) return; // a failed background poll keeps the last good data
    error.value = `Could not read the swarm: ${i.body.error ?? `HTTP ${i.status}`}`;
    return;
  }
  error.value = '';
  info.value = { ...i.body, nodes: i.body.nodes ?? [], ports: i.body.ports ?? [] };
  signals.value = s.ok ? { ...s.body, nodes: s.body.nodes ?? [], stuck: s.body.stuck ?? [] } : null;
}
poll(() => load(), 10_000);

// Only `false` is missing; null is "not checked".
const unplugged = computed(() => info.value?.nodes.filter((n) => n.lokiPlugin === false) ?? []);
const sig = (id: string) => signals.value?.nodes.find((n) => n.id === id);

// ── nodes ───────────────────────────────────────────────────────────────────────────────────────
const columns: TableColumn[] = [
  { key: 'host', label: 'Hostname' },
  { key: 'role', label: 'Role', hideBelow: 480 },
  { key: 'status', label: 'Status' },
  { key: 'availability', label: 'Availability', hideBelow: 560 },
  { key: 'tasks', label: 'Tasks', align: 'end', hideBelow: 640 },
  { key: 'engine', label: 'Engine', hideBelow: 820 },
  { key: 'short', label: 'Id', hideBelow: 960 },
];
const rows = computed<TableRow[]>(() =>
  [...named.value].map(([rid, n]) => {
    const s = sig(n.id);
    return {
      id: rid,
      host: n.hostname || '—',
      role: n.role,
      status: n.status,
      availability: n.availability || 'unknown',
      tasks: s ? String(s.tasks) : '—',
      engine: n.engineVersion || '—',
      short: n.id.slice(0, 12),
    };
  }),
);
const nodeOf = (row: TableRow) => named.value.get(row.id);

const rowActions = (row: TableRow): MenuAction[] => {
  const n = nodeOf(row);
  if (!n) return [];
  const drained = n.availability === 'drain';
  return [
    drained ? { id: 'undrain', label: 'Activate' } : { id: 'drain', label: 'Drain' },
    { id: 'remove', label: 'Remove', danger: true, separatorBefore: true, disabled: !removable({ ...n, state: n.status }) },
  ];
};

const confirm = reactive({ open: false, kind: 'drain' as 'drain' | 'remove', id: '', host: '' });
const acting = ref('');
function onRowAction(e: { id: string; action: string }): void {
  const n = named.value.get(e.id);
  if (!n) return;
  if (acting.value) return;
  if (e.action === 'undrain') void act('undrain', n.id, n.hostname);
  else {
    Object.assign(confirm, { open: true, kind: e.action as 'drain' | 'remove', id: n.id, host: n.hostname || n.id.slice(0, 12) });
  }
}
async function act(kind: 'drain' | 'undrain' | 'remove', id: string, host: string): Promise<void> {
  if (acting.value) return;
  confirm.open = false;
  acting.value = id;
  const fn = kind === 'drain' ? swarmApi.drain : kind === 'undrain' ? swarmApi.undrain : swarmApi.remove;
  const r = await fn(id);
  acting.value = '';
  if (r.status === 403) toast('Maintainer only.', 'danger');
  else if (!r.ok) toast(`${host || id}: ${r.body.error ?? `HTTP ${r.status}`}`, 'danger');
  else toast(`${host || id}: ${kind === 'undrain' ? 'activated' : kind === 'drain' ? 'draining' : 'removed'}.`, 'success');
  void load(true);
}

// ── joining ─────────────────────────────────────────────────────────────────────────────────────
const FORMATS: Array<{ value: JoinFormat; label: string }> = [
  { value: 'command', label: 'docker swarm join' },
  { value: 'script', label: 'Shell script' },
  { value: 'cloud-config', label: 'cloud-config' },
  { value: 'token', label: 'Token only' },
];
const format = ref<JoinFormat>('command');
const distro = ref('ubuntu');
const revealed = ref('');
const revealing = ref(false);
const joinError = ref('');
watch([format, distro], () => {
  revealed.value = '';
  joinError.value = '';
});

async function reveal(): Promise<void> {
  revealing.value = true;
  joinError.value = '';
  revealed.value = '';
  const r = await swarmApi.join(joinPath(format.value, distro.value));
  revealing.value = false;
  if (!r.ok) joinError.value = joinProblem(r.status, r.text);
  else revealed.value = r.text;
}

// The join token is a secret: fetched on a click, held here only, gone when the page is left.
onBeforeUnmount(() => {
  revealed.value = '';
});
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Swarm" :description="info?.active && info.managerAddr ? `Manager ${info.managerAddr}` : 'The nodes previews run on.'" actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" :loading="refreshing" @click="load(true)" />
      </template>
    </HPageHeader>

    <HAlert v-if="error" tone="danger" :description="error" />

    <template v-else>
      <HAlert v-if="info && !info.reachable" tone="warning" title="Docker did not answer." description="Nodes are unknown, not none." />
      <HAlert v-else-if="info && !info.active" tone="info" title="Not a swarm manager." :description="info.note" />
      <template v-else>
        <HAlert v-if="info?.error" tone="warning" title="Docker reported a problem." :description="info.error" />

        <HHeading :level="2" size="md">Nodes</HHeading>
        <HDataTable
          label="Swarm nodes"
          :rows="rows"
          :columns="columns"
          :loading="!loaded"
          :page-size="20"
          :get-row-actions="manage ? rowActions : undefined"
          empty-text="Docker lists no nodes."
          @row-action="onRowAction"
        >
          <template #cell="{ row, column, value }">
            <template v-if="column.key === 'host'">
              {{ value }}
              <HBadge v-if="nodeOf(row)?.self" tone="info" label="This node" />
              <HBadge v-if="nodeOf(row)?.lokiPlugin === false" tone="warning" label="No loki plugin" />
              <HBadge v-if="sig(nodeOf(row)?.id ?? '')?.emptySince" tone="neutral" :label="`empty ${ago(sig(nodeOf(row)!.id)!.emptySince)}`" />
            </template>
            <HBadge v-else-if="column.key === 'role'" :label="String(value)" :tone="value === 'manager' ? 'accent' : 'neutral'" variant="outline" />
            <HBadge v-else-if="column.key === 'status'" dot :label="String(value)" :tone="value === 'ready' ? 'success' : 'danger'" />
            <HBadge v-else-if="column.key === 'availability'" :label="String(value)" :tone="value === 'active' ? 'neutral' : 'warning'" />
            <code v-else-if="column.key === 'engine' || column.key === 'short'">{{ value }}</code>
            <template v-else>{{ value }}</template>
          </template>
        </HDataTable>

        <HAlert
          v-if="unplugged.length"
          tone="warning"
          :title="`No loki plugin on ${unplugged.map((n) => n.hostname || n.id.slice(0, 12)).join(', ')}.`"
        >
          <HCodeBlock v-if="info?.lokiPluginInstall" :code="info.lokiPluginInstall" language="sh" wrap />
        </HAlert>

        <template v-if="signals?.stuck.length">
          <HHeading :level="2" size="md">Stuck services</HHeading>
          <HList label="Stuck services" divided>
            <HListItem v-for="t in signals.stuck" :key="t.task" :title="t.service" :description="t.reason" :badge="t.stack" tone="warning" />
          </HList>
        </template>

        <HHeading :level="2" size="md">Add a worker</HHeading>
        <HText tone="muted">Open these between the new machine and every other node.</HText>
        <HList v-if="info?.ports.length" label="Ports" divided>
          <HListItem v-for="p in info.ports" :key="p.port" :title="p.port" :description="p.why" />
        </HList>

        <HText v-if="!manage" tone="muted" size="sm">Maintainer only: the join command.</HText>
        <template v-else>
        <HStack direction="row" :gap="3" wrap align="end">
          <HSelect v-model="format" label="Format" :options="FORMATS" />
          <HSelect v-if="format === 'cloud-config'" v-model="distro" label="Distro" :options="DISTROS.map((d) => ({ value: d, label: d }))" />
          <HButton variant="primary" label="Reveal" :loading="revealing" @click="reveal" />
        </HStack>
        <HAlert v-if="joinError" tone="danger" title="Nothing revealed." :description="joinError" />
        <HAlert v-if="revealed" tone="danger" title="Secret." description="Anyone with this can add a node that runs tasks here. Rotate: docker swarm join-token --rotate worker">
          <HCodeBlock :code="revealed" language="sh" wrap max-height="50vh" />
          <HButton size="compact" variant="ghost" label="Hide" @click="revealed = ''" />
        </HAlert>
        </template>
      </template>
    </template>

    <HAlertDialog
      v-model:open="confirm.open"
      danger
      :title="confirm.kind === 'drain' ? `Drain ${confirm.host}?` : `Remove ${confirm.host}?`"
      :description="confirm.kind === 'drain' ? 'Its tasks move to other nodes.' : 'The node leaves the swarm.'"
      :confirm-label="confirm.kind === 'drain' ? 'Drain' : 'Remove'"
      :busy="acting !== ''"
      @confirm="act(confirm.kind, confirm.id, confirm.host)"
    />
  </HStack>
</template>
