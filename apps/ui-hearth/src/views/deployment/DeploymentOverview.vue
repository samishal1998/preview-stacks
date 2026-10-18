<script setup lang="ts">
/** The resolved spec, summarised, plus an on-demand readiness check. Read-only: actions live in the header and Danger. */
import { computed, ref } from 'vue';
import { useRouter } from 'vue-router';
import { HAlert, HBadge, HButton, HCard, HDataTable, HDescriptionList, HSkeleton, HStack, HText, type DescriptionItem, type TableColumn } from '@hearth-ui/vue';
import type { Readiness, Runtime } from '@samyx/preview-stacks-client';
import { deploymentApi, type DeploymentDetail } from '../../api-deployment';

import { problemText, sentence } from '../../lib/deployment';
import { can } from '../../lib/users';
import { auth } from '../../state';

const props = defineProps<{ id: string; dep: DeploymentDetail | null; runtime: Runtime | null; error: string; loading: boolean }>();
const router = useRouter();
const stamp = (at?: number | null) => (at ? new Date(at).toLocaleString() : '—');

const items = computed<DescriptionItem[]>(() => {
  const d = props.dep;
  if (!d) return [];
  const c = d.compose;
  const out: DescriptionItem[] = [
    { key: 'stack', label: 'Stack', value: d.stack || '—' },
    { key: 'kind', label: 'Kind', value: sentence(d.kind) },
  ];
  if (d.specName) out.push({ key: 'spec', label: 'Spec', value: d.specName });
  out.push({ key: 'file', label: 'Compose file', value: c ? c.file : 'none' });
  if (c) {
    out.push({ key: 'profiles', label: 'Profiles', value: c.profiles.join(', ') || 'none' });
    if (c.subdomains?.length)
      out.push({
        key: 'sub',
        label: 'Subdomains',
        value: c.subdomains.map((s) => `*.${s.host} → ${s.profile}${s.depth === 'any' ? ' (any depth, HTTP only)' : ''}`).join('; '),
      });
    out.push({ key: 'orch', label: 'Orchestrator', value: d.orchestrator ?? 'compose' });
  }
  if (d.sleep) {
    const parts = [d.sleep.idle && `${d.sleep.idle} idle`, d.sleep.after && `${d.sleep.after} after deploy`].filter(Boolean);
    out.push({ key: 'sleep', label: 'Sleep policy', value: parts.join(' · ') || 'none' });
  }
  if (c) out.push({ key: 'overlays', label: 'Overlays', value: c.overlays.join(', ') || 'none' });
  out.push(
    { key: 'axes', label: 'Axes', value: d.axes?.length ?? 0 },
    { key: 'requires', label: 'Requires', value: d.requires?.length ?? 0 },
    { key: 'created', label: 'Created', value: stamp(d.createdAt) },
    { key: 'updated', label: 'Updated', value: stamp(d.updatedAt) },
  );
  return out;
});

const wakeOn = computed(() => {
  const a = props.dep?.asleep;
  if (!a) return '';
  const on = [...a.hosts, ...a.rules.map((r) => `/${r}/`)];
  return on.length ? `Wakes on a request to ${on.join(', ')}.` : 'No hostnames. Only Wake brings it back.';
});

// ── readiness: on demand, because reading it starts a silent watch on the server ───────────────
const rd = ref<Readiness | null>(null);
const rdError = ref('');
const rdLoading = ref(false);
async function checkReadiness(): Promise<void> {
  rdLoading.value = true;
  rdError.value = '';
  const r = await deploymentApi.readiness(props.id);
  rdLoading.value = false;
  if (!r.ok) {
    rdError.value = problemText(r.status, r.body.error, 'Readiness');
    return;
  }
  rd.value = r.body;
}
const rdColumns: TableColumn[] = [
  { key: 'name', label: 'Container', truncate: true },
  { key: 'state', label: 'State' },
  { key: 'health', label: 'Health', hideBelow: 480 },
  { key: 'restarts', label: 'Restarts', align: 'end', hideBelow: 560 },
  { key: 'reason', label: 'Reason', hideBelow: 640 },
];
const rdRows = computed(() =>
  (rd.value?.containers ?? []).map((c) => ({
    id: c.name,
    name: c.name,
    state: c.failed ? 'Failed' : c.ready ? 'Ready' : 'Waiting',
    health: c.hasHealthcheck ? (c.health ?? '—') : 'No healthcheck',
    restarts: c.restartCount,
    reason: c.reason ?? '',
  })),
);
const rdTone = computed(() => (rd.value?.state === 'ready' ? 'success' : rd.value?.state === 'failed' || rd.value?.state === 'timedout' ? 'danger' : 'info'));
</script>

<template>
  <HStack direction="column" :gap="4">
    <HAlert v-if="error" tone="danger" title="Could not resolve this deployment" :description="error">
      <HButton variant="secondary" label="Open Config" @click="router.push(`/deployments/${encodeURIComponent(id)}/config`)" />
    </HAlert>
    <HAlert v-if="dep?.asleep" tone="info" :title="`Asleep since ${stamp(dep.asleep.since)}`" :description="`${dep.asleep.reason}. ${wakeOn}`" />
    <HAlert
      v-if="runtime?.findings.length"
      :tone="runtime.findings.some((f) => f.level === 'error') ? 'danger' : 'warning'"
      :title="`${runtime.findings.length} finding${runtime.findings.length === 1 ? '' : 's'}`"
    >
      <HButton variant="ghost" label="See containers and routes" @click="router.push(`/deployments/${encodeURIComponent(id)}/runtime`)" />
    </HAlert>

    <HCard>
      <template #header>
        <HStack direction="row" :gap="3" wrap align="center" justify="between">
          <HText weight="medium">Resolved spec</HText>
          <HStack v-if="dep && can('developer', auth)" direction="row" :gap="2" wrap>
            <HButton variant="ghost" label="Duplicate" @click="router.push({ path: '/submit', query: { from: id } })" />
            <HButton variant="ghost" label="Edit spec and compose" trailing-icon="right" @click="router.push(`/submit/${encodeURIComponent(id)}`)" />
          </HStack>
        </HStack>
      </template>
      <HSkeleton v-if="loading && !dep" variant="rectangle" height="10rem" label="Loading spec" />
      <HText v-else-if="!dep" tone="muted">Spec not resolved.</HText>
      <HDescriptionList v-else :items="items" columns="auto" min-column-width="14rem" />
    </HCard>

    <HCard>
      <template #header>
        <HStack direction="row" :gap="3" wrap align="center" justify="between">
          <HStack direction="row" :gap="2" align="center">
            <HText weight="medium">Readiness</HText>
            <HBadge v-if="rd" :label="sentence(rd.state)" :tone="rdTone" dot />
          </HStack>
          <HButton variant="secondary" icon="arrow" label="Check" :loading="rdLoading" :disabled="!dep?.stack" @click="checkReadiness" />
        </HStack>
      </template>
      <HAlert v-if="rdError" tone="danger" :title="rdError" compact />
      <HAlert v-else-if="rd && !rd.reachable" tone="warning" title="Docker did not answer" description="Container fields are the last known." compact />
      <HDataTable v-if="rd" label="Readiness" :rows="rdRows" :columns="rdColumns" empty-text="No containers yet." />
      <HText v-else-if="!rdError" tone="muted">{{ dep?.stack ? 'Not checked.' : 'Needs variables.' }}</HText>
    </HCard>
  </HStack>
</template>
