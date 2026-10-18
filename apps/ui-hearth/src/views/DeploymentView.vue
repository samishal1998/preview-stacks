<script setup lang="ts">
/**
 * One deployment: what it is, what runs for it, its logs, its jobs, and the things that destroy it.
 * HResourceDetail owns breadcrumbs, state, metadata and tabs; each tab body is its own component.
 * The page polls detail and runtime together, so every tab reads the same snapshot.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { HAlert, HBadge, HButton, HEmptyState, HErrorPage, HResourceDetail, HSkeleton, HStack, HText, HTimeline, type TimelineItem } from '@hearth-ui/vue';
import type { Runtime } from '@samyx/preview-stacks-client';
import { deploymentApi, type DeploymentDetail } from '../api-deployment';
import { ACTION_LABEL, ago, jobTone, took } from '../format';
import { problemText, runState, sentence } from '../lib/deployment';
import { useDeploymentActions } from '../lib/use-deployment-actions';
import { can } from '../lib/users';
import { auth, data } from '../state';
import { detail as varsDetail } from '../vars/useDetail';
import { readVars } from '../vars/store';
import { varsQuery } from '../lib/vars';
import AxesTab from './deployment/AxesTab.vue';
import ConfigTab from './deployment/ConfigTab.vue';
import ConflictNote from './deployment/ConflictNote.vue';
import DeploymentDanger from './deployment/DeploymentDanger.vue';
import DeploymentLogs from './deployment/DeploymentLogs.vue';
import DeploymentOverview from './deployment/DeploymentOverview.vue';
import RequiresTab from './deployment/RequiresTab.vue';
import RuntimeTab from './deployment/RuntimeTab.vue';
import TerminalTab from './deployment/TerminalTab.vue';

const props = defineProps<{ id: string; tab?: string }>();
const router = useRouter();

const dep = ref<DeploymentDetail | null>(null);
const runtime = ref<Runtime | null>(null);
const missing = ref(false);
const loading = ref(true);
const refreshing = ref(false);
/** Why the spec did not resolve, or any other failure of the detail call. The 400 names the missing variable. */
const detailError = ref('');
const runtimeError = ref('');

const actions = useDeploymentActions(() => props.id, dep);

async function load(): Promise<void> {
  const mine = props.id;
  const [d, r] = await Promise.all([deploymentApi.detail(mine), deploymentApi.runtime(mine)]);
  if (mine !== props.id) return; // navigated away mid-flight: never show one deployment under another's id
  loading.value = false;
  if (d.status === 404) {
    missing.value = true;
    if (poll) clearInterval(poll);
    return;
  }
  if (d.ok) {
    dep.value = d.body;
    detailError.value = '';
  } else if (dep.value && (d.status === 0 || d.status >= 500)) {
    // A blip on a poll keeps what is shown; only a real answer (400 unresolved, 403) replaces it.
    detailError.value = problemText(d.status, d.body.error, 'Deployment');
  } else {
    dep.value = null;
    detailError.value = problemText(d.status, d.body.error, 'Deployment');
  }
  if (r.ok) {
    runtime.value = r.body;
    runtimeError.value = '';
  } else {
    runtimeError.value = problemText(r.status, r.body.error, 'Runtime');
  }
}
async function reload(): Promise<void> {
  refreshing.value = true;
  await load();
  refreshing.value = false;
}

let poll: ReturnType<typeof setInterval> | undefined;
// Config's "Apply and reload" saves the variables and re-resolves its own copy; follow it so this
// page, the runtime and the lifecycle calls all use the same variables.
let appliedVars = varsQuery(readVars(props.id));
watch(
  () => varsDetail.loading,
  (l) => {
    if (l || varsDetail.id !== props.id) return;
    const q = varsQuery(readVars(props.id));
    if (q !== appliedVars) {
      appliedVars = q;
      void load();
    }
  },
);
watch(
  () => props.id,
  () => {
    appliedVars = varsQuery(readVars(props.id));
    dep.value = null;
    runtime.value = null;
    missing.value = false;
    loading.value = true;
    detailError.value = '';
    runtimeError.value = '';
    actions.reset();
    void load();
    if (poll) clearInterval(poll);
    // A hidden tab does not poll: the host is running everyone else's previews too.
    poll = setInterval(() => !document.hidden && void load(), 6000);
  },
  { immediate: true },
);
onBeforeUnmount(() => poll && clearInterval(poll));

// 'containers' was folded into the runtime tab; old links keep working.
const tab = computed({
  get: () => (props.tab === 'containers' ? 'runtime' : (props.tab ?? 'overview')),
  set: (v: string) => void router.replace(`/deployments/${encodeURIComponent(props.id)}/${v}`),
});
const allTabs = [
  { value: 'overview', label: 'Overview' },
  { value: 'runtime', label: 'Containers and routes' },
  { value: 'config', label: 'Config and variables' },
  { value: 'axes', label: 'Axes' },
  { value: 'requires', label: 'Requires' },
  { value: 'logs', label: 'Logs' },
  { value: 'terminal', label: 'Terminal' },
  { value: 'jobs', label: 'Jobs' },
  { value: 'danger', label: 'Danger' },
];
// Courtesy: the shell and the lifecycle actions are developer's. The server still decides.
const tabs = computed(() => (can('developer', auth) ? allTabs : allTabs.filter((t) => t.value !== 'terminal' && t.value !== 'danger')));

// The fix for an unresolved spec lives on Config, so land there, but only from the tab with nothing to show.
watch(detailError, (e) => {
  if (e && !props.tab) void router.replace(`/deployments/${encodeURIComponent(props.id)}/config`);
});

// The detail call does not say whether the stack is up; the list row does.
const state = computed(() => (dep.value ? runState({ busy: dep.value.busy, asleep: dep.value.asleep, running: data.deployments.find((d) => d.id === props.id)?.running ?? null }) : { label: detailError.value ? 'Unresolved' : 'Loading', tone: 'neutral' as const }));
const shared = computed(() => dep.value?.kind === 'shared');
const fields = computed(() => [
  { key: 'stack', label: 'Stack', value: dep.value?.stack ?? (detailError.value ? 'unresolved' : '—') },
  { key: 'kind', label: 'Kind', value: dep.value ? sentence(dep.value.kind) : '—' },
  { key: 'orchestrator', label: 'Orchestrator', value: dep.value?.orchestrator ?? 'none' },
  { key: 'spec', label: 'Spec', value: dep.value?.specName ?? 'inline' },
  { key: 'updated', label: 'Updated', value: ago(dep.value?.updatedAt) },
]);

// ── header actions ──────────────────────────────────────────────────────────────────────────────
const primary = computed(() => (dep.value?.asleep ? 'wake' : 'up'));
const blocked = computed(() => actions.why(primary.value));
const { pending } = actions;

// ── jobs ────────────────────────────────────────────────────────────────────────────────────────
const mine = computed(() => data.jobs.filter((j) => j.deployment === props.id));
const timeline = computed<TimelineItem[]>(() =>
  mine.value.map((j) => ({
    id: j.id,
    title: ACTION_LABEL[j.action] ?? j.action,
    description: `${sentence(j.state)}${j.endedAt ? ` in ${took(j.startedAt, j.endedAt)}` : ''}`,
    timestamp: ago(j.startedAt),
    href: `/jobs/${encodeURIComponent(j.id)}`,
    tone: jobTone(j.state),
  })),
);
</script>

<template>
  <HSkeleton v-if="loading" variant="rectangle" label="Loading deployment" />
  <HErrorPage v-else-if="missing" kind="not-found" brand="pstack" home-href="/deployments" :title="`No deployment ${id}`" description="It may have been forgotten." />

  <HResourceDetail
    v-else
    :title="id"
    :description="dep?.stack ?? (detailError ? 'unresolved' : '')"
    :breadcrumbs="[{ id: 'deployments', label: 'Deployments' }, { id: 'self', label: id }]"
    :status="state.label"
    :tone="state.tone"
    :fields="fields"
    :tabs="tabs"
    v-model:active-tab="tab"
    @navigate="router.push('/deployments')"
  >
    <template #actions>
      <HButton variant="ghost" icon="arrow" label="Re-resolve" :loading="refreshing" @click="reload" />
      <HButton
        :label="primary === 'wake' ? 'Wake' : 'Deploy'"
        :icon="primary === 'wake' ? 'sun' : 'up'"
        :loading="pending === primary"
        :disabled="!!pending || !!blocked"
        @click="actions.act(primary)"
      />
      <HButton variant="secondary" icon="check" label="Verify" :loading="pending === 'verify'" :disabled="!!pending || !!actions.why('verify')" @click="actions.act('verify')" />
    </template>

    <template #summary>
      <HStack direction="column" :gap="2">
        <HStack direction="row" :gap="2" wrap align="center">
          <HBadge v-if="dep?.orchestrator === 'swarm'" label="Swarm" />
          <HText v-if="shared" tone="muted" size="sm">Shared with every preview on this host.</HText>
          <HText v-if="blocked && dep" tone="muted" size="sm">{{ blocked }}</HText>
        </HStack>
        <HAlert v-if="actions.error.value" tone="danger" :title="actions.error.value" />
        <ConflictNote v-if="actions.conflict.value" :conflict="actions.conflict.value" :job-id="actions.conflictJobId.value" />
      </HStack>
    </template>

    <template #overview><DeploymentOverview :id="id" :dep="dep" :runtime="runtime" :error="detailError" :loading="loading" /></template>
    <template #config><ConfigTab :id="id" :dep="dep" :runtime="runtime" /></template>
    <template #runtime><RuntimeTab :id="id" :dep="dep" :runtime="runtime" :error="runtimeError" :loading="refreshing" @refresh="reload" /></template>
    <template #axes><AxesTab :id="id" :dep="dep" :runtime="runtime" /></template>
    <template #requires><RequiresTab :id="id" :dep="dep" :runtime="runtime" /></template>
    <template #terminal><TerminalTab :id="id" :dep="dep" :runtime="runtime" :error="runtimeError" /></template>
    <template #logs><DeploymentLogs :id="id" :dep="dep" :runtime="runtime" /></template>

    <template #jobs>
      <HEmptyState v-if="!timeline.length" title="No jobs for this deployment" icon="clock" />
      <HTimeline v-else :items="timeline" label="Jobs" />
    </template>

    <template #danger><DeploymentDanger :id="id" :dep="dep" :actions="actions" /></template>
  </HResourceDetail>
</template>
