<script setup lang="ts">
/**
 * Compose logs, read on demand or followed live. Never polled: an auto-refresh against a chatty
 * stack would hammer the host that runs everyone else's previews. The tail is clamped by the server.
 */
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { HAlert, HButton, HEmptyState, HLogViewer, HSelect, HStack, HSwitch, HText, type LogEntry } from '@hearth-ui/vue';
import type { Runtime } from '@samyx/preview-stacks-client';
import type { DeploymentDetail } from '../../api-deployment';
import { containersWithServices, filterLines, logFileName, logLevel, parseLogLine } from '../../lib/deployment';
import { exploreUrl, serviceQuery, showGrafana } from '../../lib/grafana';
import { can } from '../../lib/users';
import { auth, grafana } from '../../state';
import { MAX_FOLLOW_LINES, useLogs } from '../../lib/use-logs';

const props = defineProps<{ id: string; dep: DeploymentDetail | null; runtime: Runtime | null }>();
const route = useRoute();
const router = useRouter();

const selected = ref(''); // '' = the whole stack, else a container name
const tail = ref('200');
const since = ref('');
const timestamps = ref(false);
const query = ref('');

const containers = computed(() => (props.runtime ? containersWithServices(props.runtime) : []));
const current = computed(() => containers.value.find((c) => c.name === selected.value) ?? null);
// Compose narrows by service, not by container.
const service = computed(() => current.value?.service ?? '');
const tailN = computed(() => Number(tail.value));

// The chosen service in Grafana. Never on share links: PublicLogsView does not render this pane.
const grafanaHref = computed(() =>
  props.dep?.stack && service.value && showGrafana(grafana.value, can('developer', auth), auth.viaToken) ? exploreUrl(grafana.value, serviceQuery(props.dep.stack, service.value)) : '',
);

const logs = useLogs({ id: () => props.id, service, timestamps, tail: tailN, since });

const containerOptions = computed(() => [
  { value: '', label: 'All services' },
  ...containers.value.map((c) => ({ value: c.name, label: c.service && c.service !== c.name ? `${c.service} (${c.name})` : c.name })),
]);
const TAILS = ['50', '200', '500', '1000', '2000'].map((v) => ({ value: v, label: `${v} lines` }));
const SINCES = [
  { value: '', label: 'Anything kept' },
  { value: '5m', label: 'Last 5 minutes' },
  { value: '30m', label: 'Last 30 minutes' },
  { value: '1h', label: 'Last hour' },
  { value: '24h', label: 'Last day' },
];

const hasCompose = computed(() => !!props.dep?.compose);
const swarm = computed(() => props.dep?.orchestrator === 'swarm');

function select(name: string): void {
  selected.value = name;
  // Keep the URL honest so the pane can be linked to and survives a reload.
  void router.replace({ query: { ...route.query, container: name || undefined } });
  if (!logs.following.value && hasCompose.value) void logs.fetchOnce();
}

// ?container= is how the Containers table jumps here. Applied once the name resolves to a container,
// and it fetches at once: arriving from "show me its logs" and then pressing Fetch is a wasted click.
watch(
  [containers, () => route.query.container],
  ([list, wanted]) => {
    const name = typeof wanted === 'string' ? wanted : '';
    if (!name || selected.value === name || !list.some((c) => c.name === name)) return;
    selected.value = name;
    if (hasCompose.value) void logs.fetchOnce();
  },
  { immediate: true },
);

watch(
  () => props.id,
  () => {
    selected.value = '';
    query.value = '';
    logs.reset();
  },
);

const entries = computed<LogEntry[]>(() => {
  const only = current.value?.name ?? '';
  const names = new Set(containers.value.map((c) => c.name));
  const out: LogEntry[] = [];
  logs.lines.value.forEach((line, i) => {
    const p = parseLogLine(line);
    // A service with several replicas answers as one stream; keep only the chosen replica's lines.
    if (only && p.service && p.service !== only && names.has(p.service)) return;
    out.push({
      id: String(i),
      message: p.service && !only ? `${p.service}  ${p.message}` : p.message,
      level: logLevel(p.message),
      ...(p.time ? { timestamp: new Date(p.time).toTimeString().slice(0, 8) } : {}),
    });
  });
  return out;
});

function download(): void {
  const text = filterLines(entries.value.map((e) => e.message), query.value).join('\n');
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = logFileName(props.id, current.value?.service ?? '');
  a.click();
  URL.revokeObjectURL(url);
}
</script>

<template>
  <HEmptyState v-if="!dep" title="Unavailable" description="The spec has not resolved." icon="info" />
  <HEmptyState v-else-if="!hasCompose" title="No compose section" description="Nothing to read logs from." icon="info" />

  <HStack v-else direction="column" :gap="3">
    <HStack direction="row" :gap="3" wrap align="end" style="--h-choice-height: var(--h-control-height)">
      <HSelect :model-value="selected" label="Container" :options="containerOptions" @change="select" />
      <HSelect v-model="tail" label="Lines" :options="TAILS" />
      <HSelect v-model="since" label="Since" :options="SINCES" />
      <HSwitch v-model="timestamps" label="Timestamps" />
      <HButton :variant="logs.following.value ? 'danger' : 'secondary'" :label="logs.following.value ? 'Stop following' : 'Follow'" @click="logs.toggle" />
      <HButton :label="logs.touched.value ? 'Refresh' : 'Fetch'" icon="arrow" :loading="logs.loading.value" :disabled="logs.following.value" @click="logs.fetchOnce" />
      <HButton variant="ghost" icon="down" label="Download" :disabled="!entries.length" @click="download" />
      <HButton v-if="grafanaHref" variant="ghost" icon="launch" label="Grafana" :href="grafanaHref" target="_blank" />
    </HStack>

    <HAlert v-if="logs.error.value" tone="danger" :title="logs.error.value" />
    <!-- ok:false means compose exited non-zero: usually nothing brought up yet, or asleep. A note, not a fault. -->
    <HAlert
      v-else-if="logs.touched.value && !logs.following.value && !logs.lastOk.value"
      tone="warning"
      :title="`${swarm ? 'docker service logs' : 'Compose'} exited non-zero`"
      :description="dep.asleep ? 'Asleep. No containers to read.' : 'Usually nothing is brought up yet. What it printed is below.'"
      compact
    />
    <HAlert v-if="logs.note.value" tone="info" :title="logs.note.value" compact />

    <HLogViewer
      v-if="logs.touched.value"
      :entries="entries"
      :follow="logs.following.value"
      :max-lines="MAX_FOLLOW_LINES"
      label="Deployment logs"
      wrap
      @search-change="(q: string) => (query = q)"
    />
    <HText v-else tone="muted">Fetch the last lines, or follow live.</HText>

    <HText v-if="logs.following.value" tone="muted" size="sm">Following {{ current ? current.name : 'the whole stack' }}. Last {{ MAX_FOLLOW_LINES.toLocaleString() }} lines kept.</HText>
    <HText v-else-if="logs.touched.value && logs.lastMeta.value" tone="muted" size="sm">{{ logs.lastMeta.value }}. Secrets redacted on the host.</HText>
  </HStack>
</template>
