<script setup lang="ts">
/**
 * The page a share link opens: /deployments/:id/public-logs-view?token=...
 *
 * A visitor has no account and no session. The link's token, a signed expiring grant to one
 * deployment, rides on every request this page makes, including the EventSource, which is why it
 * travels in the query string. It is held in memory only. Nothing here acts: the server would
 * answer 403 if anything tried. Rendered outside the app shell.
 */
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { HAlert, HBadge, HDataTable, HDescriptionList, HHeading, HLogViewer, HPublicShell, HSelect, HSkeleton, HStack, HSwitch, HText, type DescriptionItem, type LogEntry, type TableColumn } from '@hearth-ui/vue';
import type { Runtime } from '@samyx/preview-stacks-client';
import { deploymentApi, type DeploymentDetail } from '../api-deployment';
import { stamp } from '../format';
import { logLevel, parseLogLine, sentence, type Tone } from '../lib/deployment';
import { MAX_FOLLOW_LINES, useLogs } from '../lib/use-logs';

const props = defineProps<{ id: string }>();
const route = useRoute();

const token = computed(() => String(route.query.token ?? ''));
const views = ref<string[]>([]);
const expiresAt = ref<number | null>(null);
const fatal = ref('');
const ready = ref(false);
const detail = ref<DeploymentDetail | null>(null);
const runtime = ref<Runtime | null>(null);
const detailError = ref('');

const service = ref('');
const timestamps = ref(false);
const follow = ref(true);
// The public view is fixed to the last 500 lines: no control for it.
const tail = ref(500);
const since = ref('');
const logs = useLogs({ id: () => props.id, token: () => token.value, service, timestamps, tail, since });

const services = computed(() => [...new Set((runtime.value?.containers ?? []).map((c) => c.service).filter((s): s is string => !!s))]);
const serviceOptions = computed(() => [{ value: '', label: 'All services' }, ...services.value.map((s) => ({ value: s, label: s }))]);

function explain(status: number, what: string): string {
  if (status === 401) return 'This link has expired or is not valid.';
  if (status === 403) return `This link does not allow ${what}.`;
  if (status === 0) return 'The control plane is unreachable.';
  return `Could not load ${what} (HTTP ${status}).`;
}

function refreshLogs(): void {
  if (follow.value) logs.start();
  else void logs.fetchOnce();
}

async function load(): Promise<void> {
  if (!token.value) {
    fatal.value = 'This link is incomplete. It carries no token.';
    return;
  }
  const me = await deploymentApi.me(token.value);
  if (!me.ok) fatal.value = explain(me.status, 'this page');
  else if (!me.body.share) fatal.value = 'This is not a share link.';
  else if (me.body.share.deployment !== props.id) fatal.value = 'This link belongs to a different deployment.';
  if (fatal.value) return;
  views.value = me.body.share!.views;
  expiresAt.value = me.body.share!.expiresAt ?? null;
  ready.value = true;

  if (views.value.includes('details')) {
    const [d, r] = await Promise.all([deploymentApi.detail(props.id, token.value), deploymentApi.runtime(props.id, token.value)]);
    if (d.ok) detail.value = d.body;
    else detailError.value = explain(d.status, 'details');
    if (r.ok) runtime.value = r.body;
  }
  if (views.value.includes('logs')) refreshLogs();
}

const state = computed<{ label: string; tone: Tone } | null>(() => {
  if (detail.value?.asleep) return { label: 'Asleep', tone: 'neutral' };
  if (runtime.value && runtime.value.reachable === false) return { label: 'Unknown', tone: 'warning' };
  if (runtime.value?.containers.some((c) => c.state === 'running')) return { label: 'Running', tone: 'success' };
  return runtime.value ? { label: 'Not running', tone: 'neutral' } : null;
});

const items = computed<DescriptionItem[]>(() => {
  const d = detail.value;
  if (!d) return [];
  return [
    { key: 'kind', label: 'Kind', value: sentence(d.kind) },
    { key: 'orch', label: 'Orchestrator', value: d.orchestrator ?? 'compose' },
    { key: 'created', label: 'Created', value: stamp(d.createdAt) },
    { key: 'updated', label: 'Updated', value: stamp(d.updatedAt) },
    ...(d.asleep ? [{ key: 'asleep', label: 'Asleep', value: `since ${stamp(d.asleep.since)} (${d.asleep.reason})` }] : []),
  ];
});

const hasNode = computed(() => runtime.value?.containers.some((c) => c.node) ?? false);
const columns = computed<TableColumn[]>(() => [
  { key: 'service', label: 'Service' },
  { key: 'name', label: 'Container', truncate: true, hideBelow: 520 },
  { key: 'state', label: 'State' },
  { key: 'health', label: 'Health', hideBelow: 480 },
  ...(hasNode.value ? [{ key: 'node', label: 'Node', hideBelow: 640 }] : []),
]);
const rows = computed(() =>
  (runtime.value?.containers ?? []).map((c) => ({
    id: c.id,
    service: c.service ?? '—',
    name: c.name,
    state: sentence(c.state),
    tone: (c.state === 'running' ? 'success' : 'danger') as Tone,
    health: c.health ? sentence(c.health) : '—',
    node: c.node ?? '—',
  })),
);

const entries = computed<LogEntry[]>(() =>
  logs.lines.value.map((line, i) => {
    const p = parseLogLine(line);
    return {
      id: String(i),
      message: p.service ? `${p.service}  ${p.message}` : p.message,
      level: logLevel(p.message),
      ...(p.time ? { timestamp: new Date(p.time).toTimeString().slice(0, 8) } : {}),
    };
  }),
);

const canRead = () => ready.value && views.value.includes('logs');
watch(follow, () => canRead() && refreshLogs());
// While following, useLogs reopens the stream itself; a one-off read needs asking again.
watch([service, timestamps], () => canRead() && !follow.value && void logs.fetchOnce());
onMounted(load);
</script>

<template>
  <HPublicShell brand="pstack" brand-href="#" footer-text="pstack. This page can only read.">
    <HStack direction="column" :gap="4">
      <HStack direction="row" :gap="3" wrap align="center" justify="between">
        <div>
          <HText tone="muted" size="sm">Shared deployment, read-only</HText>
          <div><HHeading :level="1" size="lg">{{ id }}</HHeading></div>
          <HText v-if="detail" tone="muted" size="sm">Stack {{ detail.stack }}</HText>
          <HText v-if="expiresAt" tone="muted" size="sm"> · link expires {{ stamp(expiresAt) }}</HText>
        </div>
        <HBadge v-if="state" dot :label="state.label" :tone="state.tone" />
      </HStack>

      <HAlert v-if="fatal" tone="danger" :title="fatal" description="Ask whoever shared it for a new link." />
      <HSkeleton v-else-if="!ready" variant="rectangle" label="Loading" />

      <template v-else>
        <template v-if="views.includes('details')">
          <HDescriptionList v-if="detail" :items="items" columns="auto" min-column-width="12rem" />
          <HText v-else tone="muted">{{ detailError || 'Details could not be loaded.' }}</HText>
          <HAlert v-if="runtime && runtime.reachable === false" tone="warning" title="Docker did not answer" description="What is running could not be listed. That is not the same as nothing running." />
          <HDataTable v-else-if="runtime" label="Containers" :rows="rows" :columns="columns" empty-text="Nothing is running.">
            <template #cell="{ row, column, value }">
              <HBadge v-if="column.key === 'state'" dot :label="String(value)" :tone="row.tone as Tone" />
              <template v-else>{{ value }}</template>
            </template>
          </HDataTable>
        </template>

        <HStack v-if="views.includes('logs')" direction="column" :gap="3">
          <HStack direction="row" :gap="4" wrap align="end">
            <HSelect v-if="services.length" v-model="service" label="Service" :options="serviceOptions" />
            <HSwitch v-model="timestamps" label="Timestamps" />
            <HSwitch v-model="follow" label="Follow" />
          </HStack>
          <HAlert v-if="logs.error.value" tone="danger" :title="logs.error.value" />
          <HAlert v-else-if="logs.touched.value && !logs.following.value && !logs.lastOk.value" tone="warning" title="The log command exited non-zero" description="The stack may be asleep or not deployed." compact />
          <HAlert v-if="logs.note.value" tone="info" :title="logs.note.value" description="Reload to reconnect. The link may have expired." compact />
          <HLogViewer :entries="entries" :follow="logs.following.value" :max-lines="MAX_FOLLOW_LINES" label="Deployment logs" wrap />
        </HStack>
      </template>
    </HStack>
  </HPublicShell>
</template>
