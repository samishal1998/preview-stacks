<script setup lang="ts">
/**
 * One deployment: what it is, what is running for it, its logs, its jobs, and the things that
 * destroy it. Laid out with HResourceDetail — breadcrumbs, state, metadata and tabs are its job.
 */
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HCard,
  HCopyField,
  HDataTable,
  HDropdownMenu,
  HEmptyState,
  HErrorPage,
  HInput,
  HLogViewer,
  HResourceDetail,
  HSelect,
  HSkeleton,
  HStack,
  HSwitch,
  HText,
  HTimeline,
  type LogEntry,
  type TableColumn,
  type TimelineItem,
} from '@hearth-ui/vue';
import type { DeploymentRow, Runtime } from '@samyx/preview-stacks-client';
import { api, type Lifecycle } from '../api';
import { ACTION_LABEL, ago, containerTone, jobTone, took } from '../format';
import { data, refresh, toast } from '../state';

const props = defineProps<{ id: string; tab?: string }>();
const router = useRouter();

const logs = reactive({ service: '', timestamps: false, entries: [] as LogEntry[], loading: false, error: '', note: '' });
const dep = ref<DeploymentRow | null>(null);
const runtime = ref<Runtime | null>(null);
const missing = ref(false);
const loading = ref(true);

async function load(): Promise<void> {
  const [d, r] = await Promise.all([api.deployment(props.id), api.runtime(props.id)]);
  if (d.status === 404) {
    missing.value = true;
    loading.value = false;
    return;
  }
  if (d.ok) dep.value = d.body;
  if (r.ok) runtime.value = r.body;
  loading.value = false;
}
let poll: ReturnType<typeof setInterval> | undefined;
watch(
  () => props.id,
  () => {
    dep.value = null;
    runtime.value = null;
    missing.value = false;
    loading.value = true;
    logs.entries = [];
    void load();
    if (poll) clearInterval(poll);
    poll = setInterval(() => void load(), 6000);
  },
  { immediate: true },
);
onBeforeUnmount(() => poll && clearInterval(poll));

const tab = computed({
  get: () => props.tab ?? 'overview',
  set: (v: string) => void router.replace(`/deployments/${encodeURIComponent(props.id)}/${v}`),
});
const tabs = [
  { value: 'overview', label: 'Overview' },
  { value: 'containers', label: 'Containers' },
  { value: 'logs', label: 'Logs' },
  { value: 'jobs', label: 'Jobs' },
  { value: 'danger', label: 'Danger' },
];

const status = computed(() => (dep.value?.asleep ? 'asleep' : dep.value?.busy ? 'busy' : dep.value?.running ? 'running' : 'idle'));
const tone = computed(() => (status.value === 'running' ? 'success' : status.value === 'busy' ? 'info' : 'neutral'));
const fields = computed(() => [
  { key: 'stack', label: 'Stack', value: dep.value?.stack ?? 'needs variables' },
  { key: 'kind', label: 'Kind', value: dep.value?.kind },
  { key: 'orchestrator', label: 'Orchestrator', value: dep.value?.orchestrator ?? 'none' },
  { key: 'spec', label: 'Spec', value: dep.value?.specName ?? 'inline' },
  { key: 'updated', label: 'Updated', value: ago(dep.value?.updatedAt) },
]);

// ── actions ─────────────────────────────────────────────────────────────────────────────────────
const acting = ref('');
async function act(action: Lifecycle, opts: { forget?: boolean; force?: boolean } = {}): Promise<void> {
  acting.value = opts.forget ? 'down-forget' : action;
  const r = await api.act(props.id, action, opts);
  acting.value = '';
  const label = opts.forget ? 'Tear down and forget' : ACTION_LABEL[action]!;
  if (r.status === 202 && r.body.job) {
    toast(`${label} ${r.body.job.state === 'queued' ? 'queued' : 'started'}`, 'success', dep.value?.stack ?? undefined);
    void refresh();
    void router.push(`/jobs/${encodeURIComponent(r.body.job.id)}`);
  } else {
    toast(`${label} refused`, 'danger', r.body.error ?? `HTTP ${r.status}`);
  }
}

const more = computed(() => [
  { id: 'sleep', label: 'Sleep', icon: 'moon', disabled: !!dep.value?.asleep || dep.value?.kind === 'shared' || !dep.value?.orchestrator },
  { id: 'wake', label: 'Wake', icon: 'sun', disabled: !dep.value?.asleep },
]);

const confirm = reactive({ open: false, forget: false, typed: '' });
const shared = computed(() => dep.value?.kind === 'shared');
const canConfirm = computed(() => !shared.value || confirm.typed === dep.value?.stack);
function askTeardown(forget: boolean): void {
  confirm.forget = forget;
  confirm.typed = '';
  confirm.open = true;
}

// ── containers ──────────────────────────────────────────────────────────────────────────────────
const containerColumns: TableColumn[] = [
  { key: 'service', label: 'Service', sortable: true },
  { key: 'state', label: 'State', sortable: true },
  { key: 'image', label: 'Image', truncate: true, hideBelow: 640 },
  { key: 'node', label: 'Node', hideBelow: 760 },
  { key: 'restarts', label: 'Restarts', align: 'end', hideBelow: 520 },
];
const containerRows = computed(() => {
  const r = runtime.value;
  if (!r) return [];
  const have = new Set(r.containers.map((c) => c.service));
  const rows = r.containers.map((c) => ({
    id: c.id,
    service: c.service ?? c.name,
    state: c.health ? `${c.state} · ${c.health}` : c.state,
    tone: containerTone(c.state, c.health),
    image: c.image,
    node: c.node ?? '—',
    restarts: c.restartCount,
  }));
  // A swarm service whose tasks all failed has no container, but it is the one worth looking at.
  for (const s of r.services ?? []) {
    if (!have.has(s)) rows.push({ id: `service:${s}`, service: s, state: 'no task running', tone: 'danger', image: '', node: '—', restarts: 0 });
  }
  return rows;
});
const routeColumns: TableColumn[] = [
  { key: 'hosts', label: 'Hostname' },
  { key: 'service', label: 'Service', hideBelow: 520 },
  { key: 'target', label: 'Target', hideBelow: 640 },
];
const routeRows = computed(() =>
  (runtime.value?.routes ?? []).map((r) => ({
    id: r.router,
    hosts: r.hosts.join(', ') || r.rule,
    service: r.service ?? '—',
    target: r.target ?? (r.targetReason ? r.targetReason : '—'),
  })),
);

// ── logs ────────────────────────────────────────────────────────────────────────────────────────
const serviceOptions = computed(() => [
  { value: '', label: 'All services' },
  ...(runtime.value?.services ?? [...new Set((runtime.value?.containers ?? []).map((c) => c.service ?? c.name))]).map((s) => ({ value: s, label: s })),
]);
async function fetchLogs(): Promise<void> {
  logs.loading = true;
  logs.error = '';
  const r = await api.logs(props.id, { service: logs.service || undefined, timestamps: logs.timestamps });
  logs.loading = false;
  if (!r.ok) {
    logs.error = r.body.error ?? `HTTP ${r.status}`;
    return;
  }
  const lines = (r.body.text ?? '').split('\n').filter((l) => l.trim());
  logs.entries = lines.map((message, i) => ({
    id: String(i),
    message,
    level: /\b(error|fatal|panic|exception)\b/i.test(message) ? 'error' : /\bwarn(ing)?\b/i.test(message) ? 'warning' : 'info',
  }));
  logs.note = r.body.ok ? '' : 'The log command exited non-zero; what it printed is shown.';
}
watch(
  () => tab.value,
  (t) => {
    if (t === 'logs' && !logs.entries.length) void fetchLogs();
  },
  { immediate: true },
);

// ── jobs ────────────────────────────────────────────────────────────────────────────────────────
const mine = computed(() => data.jobs.filter((j) => j.deployment === props.id));
const timeline = computed<TimelineItem[]>(() =>
  mine.value.map((j) => ({
    id: j.id,
    title: ACTION_LABEL[j.action] ?? j.action,
    description: `${j.state}${j.endedAt ? ` in ${took(j.startedAt, j.endedAt)}` : ''}`,
    timestamp: ago(j.startedAt),
    href: `/jobs/${encodeURIComponent(j.id)}`,
    tone: jobTone(j.state),
  })),
);
</script>

<template>
  <HSkeleton v-if="loading" variant="rectangle" />
  <HErrorPage v-else-if="missing" kind="not-found" brand="pstack" home-href="/deployments" :title="`No deployment ${id}`" description="It may have been forgotten." />

  <HResourceDetail
    v-else
    :title="id"
    :description="dep?.stack ?? 'needs variables'"
    :breadcrumbs="[{ id: 'deployments', label: 'Deployments' }, { id: 'self', label: id }]"
    :status="status"
    :tone="tone"
    :fields="fields"
    :tabs="tabs"
    v-model:active-tab="tab"
    @navigate="router.push('/deployments')"
  >
    <template #actions>
      <HButton icon="up" label="Deploy" :loading="acting === 'up'" :disabled="!!acting || !dep?.stack" @click="act('up')" />
      <HButton variant="secondary" icon="check" label="Verify" :loading="acting === 'verify'" :disabled="!!acting || !dep?.stack" @click="act('verify')" />
      <HDropdownMenu label="More" :items="more" @select="(a: string) => act(a as Lifecycle)" />
    </template>

    <template #overview>
      <HStack direction="column" :gap="4">
        <HAlert v-if="dep?.unresolved" tone="warning" title="This spec cannot be resolved" :description="dep.unresolved" />
        <HAlert v-if="dep?.asleep" tone="info" title="Asleep" description="A request to its hostname wakes it." />
        <HCard title="Routes" description="Hostnames Traefik was told about.">
          <HEmptyState v-if="!routeRows.length" title="No routes" description="Nothing is routed to this deployment." icon="globe" />
          <HDataTable v-else label="Routes" :rows="routeRows" :columns="routeColumns" />
        </HCard>
        <HAlert
          v-for="(f, i) in runtime?.findings ?? []"
          :key="i"
          :tone="f.level === 'error' ? 'danger' : f.level === 'warn' ? 'warning' : 'info'"
          :title="f.message"
          compact
        />
      </HStack>
    </template>

    <template #containers>
      <HDataTable label="Containers" :rows="containerRows" :columns="containerColumns" empty-text="Nothing is running for this stack.">
        <template #cell="{ row, column, value }">
          <HBadge v-if="column.key === 'state'" dot :label="String(value)" :tone="(row.tone as 'success')" />
          <template v-else>{{ value }}</template>
        </template>
      </HDataTable>
    </template>

    <template #logs>
      <HStack direction="column" :gap="3">
        <HStack :gap="3" align="end" wrap>
          <HSelect v-model="logs.service" label="Service" :options="serviceOptions" @change="fetchLogs" />
          <HSwitch v-model="logs.timestamps" label="Timestamps" @change="fetchLogs" />
          <HButton variant="secondary" icon="arrow" label="Refresh" :loading="logs.loading" @click="fetchLogs" />
        </HStack>
        <HAlert v-if="logs.error" tone="danger" :title="logs.error" />
        <HAlert v-if="logs.note" tone="warning" :title="logs.note" compact />
        <HLogViewer :entries="logs.entries" label="Deployment logs" :max-lines="2000" :follow="false" />
      </HStack>
    </template>

    <template #jobs>
      <HEmptyState v-if="!timeline.length" title="No jobs for this deployment" icon="clock" />
      <HTimeline v-else :items="timeline" label="Jobs" />
    </template>

    <template #danger>
      <HStack direction="column" :gap="4">
        <HCard title="Tear down" description="Stops and removes this deployment's containers, networks and volumes, then checks for leftovers.">
          <HAlert
            v-if="shared"
            tone="warning"
            title="Shared stack"
            description="Every preview that depends on it goes with it. Type its name to enable the buttons."
          />
          <HInput v-if="shared" v-model="confirm.typed" :label="`Type ${dep?.stack}`" />
          <HStack :gap="3" wrap>
            <HButton variant="danger" label="Tear down" :loading="acting === 'down'" :disabled="!!acting || !canConfirm" @click="askTeardown(false)" />
            <HButton variant="danger" label="Tear down and forget" :loading="acting === 'down-forget'" :disabled="!!acting || !canConfirm" @click="askTeardown(true)" />
          </HStack>
          <HText tone="muted" size="sm">Tear down and forget keeps the record if anything is left behind.</HText>
        </HCard>
        <HCard v-if="dep?.stack" title="Stack name" description="Shell and compose commands on the host use this name.">
          <HCopyField :value="dep.stack" label="Stack" />
        </HCard>
      </HStack>
    </template>
  </HResourceDetail>

  <HAlertDialog
    v-model:open="confirm.open"
    danger
    :title="confirm.forget ? `Tear down and forget ${id}?` : `Tear down ${id}?`"
    description="Containers and resources are removed and checked for leftovers."
    :confirm-label="confirm.forget ? 'Tear down and forget' : 'Tear down'"
    @confirm="act('down', { forget: confirm.forget, force: shared })"
  />
</template>
