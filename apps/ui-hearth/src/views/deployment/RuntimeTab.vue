<script setup lang="ts">
/**
 * What is running, and what Traefik was told about it. This is the page that answers "the hostname
 * does not work": the routing table shows the address Traefik assembled, or the one reason it could
 * not, and the containers table has the per-container verbs.
 */
import { computed, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HCard,
  HDataTable,
  HDialog,
  HEmptyState,
  HSkeleton,
  HStack,
  HText,
  type MenuAction,
  type TableColumn,
  type TableRow,
} from '@hearth-ui/vue';
import type { DeploymentRow, Runtime, RuntimeContainer } from '@samyx/preview-stacks-client';
import { deploymentApi } from '../../api-deployment';
import { toast } from '../../state';
import { byLabel } from '../../lib/labels';
import { containerState, containersWithServices, findingLabel, findingTone, routeTarget, sortFindings, taskOf, urlFor, problemText } from '../../lib/deployment';

const props = defineProps<{ id: string; dep: DeploymentRow | null; runtime: Runtime | null; error?: string; loading?: boolean }>();
const emit = defineEmits<{ refresh: [] }>();
const router = useRouter();
const go = (tab: string, container: string) => router.push({ path: `/deployments/${encodeURIComponent(props.id)}/${tab}`, query: { container } });

const containers = computed(() => (props.runtime ? containersWithServices(props.runtime) : []));
const running = computed(() => containers.value.filter((c) => c.state === 'running').length);
const hasNode = computed(() => containers.value.some((c) => c.node));
const findings = computed(() => sortFindings(props.runtime?.findings ?? []));
const worst = computed(() => findings.value[0]?.level ?? 'info');
const showFindings = ref(false);

// ── routes ──────────────────────────────────────────────────────────────────────────────────────
const routeColumns: TableColumn[] = [
  { key: 'url', label: 'URL', minWidth: '12rem' },
  { key: 'target', label: 'Forwards to', hideBelow: 520 },
  { key: 'router', label: 'Router', truncate: true, hideBelow: 700 },
  { key: 'tls', label: 'TLS', hideBelow: 420 },
];
const routeRows = computed(() =>
  (props.runtime?.routes ?? []).map((r) => ({
    id: r.router,
    hosts: r.hosts,
    rule: r.rule,
    target: routeTarget(r),
    container: r.container,
    router: r.router,
    priority: r.priority,
    tls: r.tls ? r.certresolver || 'inherited' : '',
  })),
);

// ── containers ──────────────────────────────────────────────────────────────────────────────────
const containerColumns = computed<TableColumn[]>(() => [
  { key: 'service', label: 'Service', minWidth: '10rem' },
  ...(hasNode.value ? [{ key: 'node', label: 'Node', hideBelow: 760 }] : []),
  { key: 'state', label: 'State' },
  { key: 'ports', label: 'Ports', hideBelow: 640 },
  { key: 'networks', label: 'Networks', hideBelow: 900 },
  { key: 'image', label: 'Image', truncate: true, hideBelow: 1000 },
]);
const named = computed(() => byLabel(containers.value, (c) => c.name, (c) => c.id));
const containerRows = computed(() => [...named.value].map(([rid, c]) => ({ id: rid, c, st: containerState(c) })));
const byId = (rid: string): RuntimeContainer | undefined => named.value.get(rid);

function actionsFor(row: TableRow): MenuAction[] {
  const c = row.c as RuntimeContainer;
  const locked = !!busy.value;
  const out: MenuAction[] = [];
  if (c.id.startsWith('service:')) return [{ id: 'logs', label: 'Logs' }];
  if (!c.job && !c.remote) {
    out.push(c.state !== 'running' ? { id: 'start', label: 'Start', disabled: locked } : { id: 'stop', label: 'Stop', disabled: locked, danger: true });
    out.push({ id: 'restart', label: 'Restart', disabled: locked });
  }
  out.push({ id: 'logs', label: 'Logs' });
  if (c.state === 'running' && !c.remote && !c.job) out.push({ id: 'shell', label: 'Shell' });
  // Only while running: the router label survives a stopped container, so the URL would answer 502.
  if (c.state === 'running' && urlFor(props.runtime, c)) out.push({ id: 'open', label: 'Open', icon: 'launch' });
  return out;
}

/** One container verb in flight, as `action:name`: two `docker restart`s racing on one stack is not worth making easy. */
const busy = ref('');
const ask = reactive({ open: false, c: null as RuntimeContainer | null, action: 'stop' as 'start' | 'stop' | 'restart' });
const VERB = { start: 'Start', stop: 'Stop', restart: 'Restart' } as const;

function onRowAction(e: { id: string; action: string }): void {
  const c = byId(e.id);
  if (!c) return;
  if (e.action === 'logs') void go('logs', c.name);
  else if (e.action === 'shell') void go('terminal', c.name);
  else if (e.action === 'open') {
    const u = urlFor(props.runtime, c);
    if (u) window.open(u, '_blank', 'noopener');
  } else {
    ask.c = c;
    ask.action = e.action as 'start' | 'stop' | 'restart';
    ask.open = true;
  }
}

async function runVerb(): Promise<void> {
  const c = ask.c;
  if (!c) return;
  busy.value = `${ask.action}:${c.name}`;
  const r = await deploymentApi.container(props.id, c.name, ask.action);
  busy.value = '';
  ask.open = false;
  if (!r.ok) {
    // Docker's own words say "already started" and "no such container" better than this layer could.
    toast(`${VERB[ask.action]} refused`, 'danger', problemText(r.status, r.body.error, c.name));
    return;
  }
  toast(r.body.note ?? `${VERB[ask.action]} sent`, 'success', c.name);
  emit('refresh');
}
</script>

<template>
  <HStack direction="column" :gap="4">
    <HAlert v-if="error" tone="danger" title="Could not inspect this deployment" :description="error" />
    <HSkeleton v-if="loading && !runtime" variant="rectangle" height="12rem" label="Loading runtime" />

    <template v-else-if="runtime">
      <!-- reachable:false is not "nothing is running": a healthy stack can sit behind a Docker socket this process cannot reach. -->
      <HAlert v-if="!runtime.reachable" tone="warning" title="Docker did not answer" description="That is not the same as nothing running." />

      <template v-else>
        <HStack direction="row" :gap="3" wrap align="center">
          <HButton v-if="findings.length" variant="secondary" icon="info" :label="`${findings.length} ${findingLabel(worst).toLowerCase()}`" @click="showFindings = true" />
          <HAlert v-else-if="containers.length" tone="success" title="Nothing looks wrong" compact />
        </HStack>

        <HCard>
          <template #header>
            <HStack direction="row" :gap="3" wrap align="center" justify="between">
              <HText weight="medium">Routing</HText>
              <HStack direction="row" :gap="2" align="center" wrap>
                <HText tone="muted" size="sm">TLS: {{ runtime.challenge }}</HText>
                <HButton variant="ghost" label="Change on Control" @click="router.push('/control')" />
              </HStack>
            </HStack>
          </template>
          <HEmptyState v-if="!routeRows.length" title="No routes" description="Traefik labels come from your compose file." icon="globe" />
          <HDataTable v-else label="Routes" :rows="routeRows" :columns="routeColumns">
            <template #cell="{ row, column }">
              <template v-if="column.key === 'url'">
                <div v-for="h in (row.hosts as string[])" :key="h">
                  <a v-if="!h.startsWith('(pattern)')" :href="`https://${h}`" target="_blank" rel="noopener noreferrer">{{ h }}</a>
                  <HText v-else as="code" tone="muted">{{ h }}</HText>
                </div>
                <HText v-if="!(row.hosts as string[]).length" tone="muted">No host in the rule</HText>
              </template>
              <template v-else-if="column.key === 'target'">
                <HBadge
                  :label="(row.target as ReturnType<typeof routeTarget>).text"
                  :tone="(row.target as ReturnType<typeof routeTarget>).tone"
                  :title="(row.target as ReturnType<typeof routeTarget>).hint"
                />
                <div><HText tone="muted" size="sm">{{ row.container }}</HText></div>
              </template>
              <template v-else-if="column.key === 'router'">
                {{ row.router }}<template v-if="row.priority"> · priority {{ row.priority }}</template>
              </template>
              <template v-else-if="column.key === 'tls'">
                <HBadge v-if="row.tls" :label="String(row.tls)" tone="success" />
                <HBadge v-else label="Off" />
              </template>
            </template>
          </HDataTable>
        </HCard>

        <HCard>
          <template #header>
            <HStack direction="row" :gap="3" wrap align="center" justify="between">
              <HText weight="medium">Containers</HText>
              <HStack direction="row" :gap="2" align="center">
                <HText tone="muted" size="sm">{{ running }} of {{ containers.length }} running</HText>
                <HButton variant="ghost" icon="arrow" label="Refresh" :loading="loading" @click="emit('refresh')" />
              </HStack>
            </HStack>
          </template>
          <HEmptyState v-if="!containers.length" title="No containers" description="Nothing is running for this stack." icon="server" />
          <HDataTable
            v-else
            label="Containers"
            :rows="containerRows"
            :columns="containerColumns"
            :get-row-actions="actionsFor"
            action-display="label"
            @row-action="onRowAction"
          >
            <template #cell="{ row, column }">
              <template v-if="column.key === 'service'">
                <div>{{ (row.c as RuntimeContainer).service ?? (row.c as RuntimeContainer).name }}</div>
                <HText v-if="!(row.c as RuntimeContainer).id.startsWith('service:')" tone="muted" size="sm" :title="(row.c as RuntimeContainer).name">{{ taskOf((row.c as RuntimeContainer).name) }}</HText>
              </template>
              <template v-else-if="column.key === 'node'">
                <HStack direction="row" :gap="2" align="center" wrap>
                  {{ (row.c as RuntimeContainer).node ?? '—' }}
                  <HBadge v-if="(row.c as RuntimeContainer).remote" label="Remote" title="On another node. Logs reach it; a shell and stop/start do not." />
                </HStack>
              </template>
              <template v-else-if="column.key === 'state'">
                <HStack direction="row" :gap="2" align="center" wrap>
                  <HBadge dot :label="(row.st as ReturnType<typeof containerState>).label" :tone="(row.st as ReturnType<typeof containerState>).tone" />
                  <HBadge v-if="(row.c as RuntimeContainer).job" label="One-shot" title="No container to start, stop or shell into." />
                  <HBadge v-if="(row.c as RuntimeContainer).restartCount > 0" :label="`${(row.c as RuntimeContainer).restartCount}x restarted`" tone="warning" />
                </HStack>
              </template>
              <template v-else-if="column.key === 'ports'">
                <div v-for="p in (row.c as RuntimeContainer).ports" :key="`${p.containerPort}/${p.protocol}`">
                  {{ p.containerPort }}<HText v-if="p.hostPort" tone="muted"> ← host {{ p.hostPort }}</HText>
                </div>
                <HText v-if="!(row.c as RuntimeContainer).ports.length" tone="muted">None</HText>
              </template>
              <template v-else-if="column.key === 'networks'">
                <div v-for="n in (row.c as RuntimeContainer).networks" :key="n">{{ n }}</div>
                <HText v-if="(row.c as RuntimeContainer).ingressIp" as="code" tone="muted" size="sm">{{ (row.c as RuntimeContainer).ingressIp }}</HText>
              </template>
              <template v-else-if="column.key === 'image'">{{ (row.c as RuntimeContainer).image }}</template>
            </template>
          </HDataTable>
        </HCard>
      </template>
    </template>

    <HDialog :open="showFindings" title="What pstack noticed" @close="showFindings = false">
      <HStack direction="column" :gap="3">
        <HAlert v-for="(f, i) in findings" :key="i" :tone="findingTone(f.level)" :title="findingLabel(f.level)" :description="f.message" />
      </HStack>
    </HDialog>

    <HAlertDialog
      v-model:open="ask.open"
      :danger="ask.action !== 'start'"
      :busy="!!busy"
      :title="`${VERB[ask.action]} ${ask.c?.name ?? ''}?`"
      :description="ask.action === 'stop' ? 'This container only. It stays stopped until something starts it.' : 'This container only.'"
      :confirm-label="VERB[ask.action]"
      @confirm="runVerb"
    />
  </HStack>
</template>
