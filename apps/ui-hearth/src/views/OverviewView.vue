<script setup lang="ts">
/** The host at a glance: control stack, deployment counts, recent jobs. A leak outranks all of it. */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlert,
  HBadge,
  HButton,
  HCard,
  HDataTable,
  HEmptyState,
  HGrid,
  HList,
  HListItem,
  HPageHeader,
  HSkeleton,
  HSparkline,
  HStatCard,
  HStack,
  HText,
  type TableColumn,
} from '@hearth-ui/vue';
import { coreApi, normalizeControl, type Control } from '../api-core';
import { summarize } from '../lib/deployments';
import { problem } from '../lib/problem';
import { useNow } from '../lib/now';
import { actionLabel, ago, containerTone, jobTone, took } from '../format';
import { data, refresh } from '../state';

const router = useRouter();
const now = useNow();

const control = ref<Control | null>(null);
const controlError = ref('');
const controlLoaded = ref(false);
async function loadControl(): Promise<void> {
  const r = await coreApi.control();
  controlLoaded.value = true;
  if (!r.ok) {
    controlError.value = problem(r, 'Control stack');
    return;
  }
  controlError.value = '';
  control.value = normalizeControl(r.body);
}
let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  void loadControl();
  timer = setInterval(() => void loadControl(), 10000);
});
onBeforeUnmount(() => timer && clearInterval(timer));
const reloading = ref(false);
async function reload(): Promise<void> {
  reloading.value = true;
  await Promise.all([loadControl(), refresh()]);
  reloading.value = false;
}

const sum = computed(() => summarize(data.deployments));
const leaked = computed(() => data.jobs.filter((j) => j.state === 'leaked'));
const attention = computed(() => data.deployments.filter((d) => d.unresolved));
const recent = computed(() => data.jobs.slice(0, 8));
const durations = computed(() =>
  data.jobs
    .filter((j) => j.startedAt && j.endedAt)
    .slice(0, 20)
    .reverse()
    .map((j) => Math.round((j.endedAt! - j.startedAt!) / 1000)),
);

const cols: TableColumn[] = [
  { key: 'name', label: 'Service', truncate: true },
  { key: 'state', label: 'State' },
  { key: 'health', label: 'Health', hideBelow: 420 },
  { key: 'image', label: 'Image', truncate: true, hideBelow: 700 },
];
const serviceRows = computed(() =>
  (control.value?.services ?? []).map((s) => ({ id: s.name, name: s.name, state: s.state, health: s.health || null, image: s.image || '—' })),
);
</script>

<template>
  <HStack direction="column" :gap="6">
    <HPageHeader title="This host," accent="at a glance." variant="hero" description="Running previews, jobs in flight, failures." actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" :loading="reloading" @click="reload" />
      </template>
    </HPageHeader>

    <HAlert v-if="leaked.length" tone="danger" :title="`${leaked.length} teardown${leaked.length === 1 ? '' : 's'} left something behind`" description="Nothing else will remove it.">
      <HStack direction="row" wrap :gap="2">
        <HButton v-for="j in leaked.slice(0, 3)" :key="j.id" size="compact" variant="secondary" :label="j.stack" icon="right" @click="router.push(`/jobs/${encodeURIComponent(j.id)}`)" />
        <HButton v-if="leaked.length > 3" size="compact" variant="ghost" :label="`All ${leaked.length} leaked`" @click="router.push('/jobs?state=leaked')" />
      </HStack>
    </HAlert>

    <HAlert v-if="!data.loaded && data.error" tone="danger" title="Could not load" :description="data.error" />

    <HGrid min-column-width="22rem" :gap="4">
      <HCard title="Control stack" :description="control?.managedBy ? `Managed from the host by ${control.managedBy}.` : 'Managed from the host.'">
        <HStack direction="column" :gap="3">
          <HStack direction="row" v-if="control?.project" :gap="2"><HBadge :label="control.project" variant="outline" /></HStack>
          <HAlert v-if="controlError" tone="danger" :title="controlError" compact />
          <HSkeleton v-else-if="!controlLoaded" variant="rectangle" />
          <template v-else-if="control">
            <HAlert v-if="!control.reachable" tone="warning" title="Docker did not answer" description="State unknown. The stack may be running." compact />
            <HAlert v-else-if="control.parseError" tone="warning" title="Docker's answer could not be read" description="State unknown for this refresh." compact />
            <HAlert v-else-if="!control.services.length" tone="danger" title="Nothing is running in this project" compact />
            <HDataTable v-if="serviceRows.length" label="Control stack services" :rows="serviceRows" :columns="cols" :page-size="20">
              <template #cell="{ row, column, value }">
                <HBadge v-if="column.key === 'state' || column.key === 'health'" dot :label="String(value || '—')" :tone="containerTone(String(row.state), (row.health as string | null) ?? null)" />
                <template v-else>{{ value }}</template>
              </template>
            </HDataTable>
          </template>
          <HButton variant="ghost" size="compact" icon="arrow" label="Refresh control stack" @click="loadControl" />
        </HStack>
      </HCard>

      <HStack direction="column" :gap="4">
        <HSkeleton v-if="!data.loaded" variant="rectangle" />
        <HGrid v-else :columns="2" :gap="3">
          <HStatCard label="Total" :value="sum.total" icon="apps" tone="neutral" />
          <HStatCard label="Running" :value="sum.running" icon="check" :tone="sum.running ? 'success' : 'neutral'" />
          <HStatCard label="Busy" :value="sum.busy" icon="clock" tone="info" />
          <HStatCard label="Asleep" :value="sum.asleep" icon="moon" tone="neutral" />
          <HStatCard v-if="sum.unknown" label="Unknown" :value="sum.unknown" icon="info" tone="warning" detail="Not the same as idle." />
        </HGrid>
        <HAlert v-for="d in attention" :key="d.id" tone="warning" :title="`${d.id} needs variables`" :description="d.unresolved" compact>
          <HButton size="compact" variant="secondary" label="Set its variables" @click="router.push(`/deployments/${encodeURIComponent(d.id)}/config`)" />
        </HAlert>
      </HStack>
    </HGrid>

    <HGrid min-column-width="22rem" :gap="4">
      <HCard title="Recent jobs" description="Newest first.">
        <HAlert v-if="!data.loaded && data.error" tone="danger" :title="data.error" compact />
        <HSkeleton v-else-if="!data.loaded" variant="rectangle" />
        <HEmptyState v-else-if="!recent.length" title="No jobs yet" icon="clock" />
        <HList v-else label="Recent jobs" divided>
          <HListItem
            v-for="j in recent"
            :key="j.id"
            interactive
            :title="`${actionLabel(j.action)} ${j.stack}`"
            :description="`${ago(j.startedAt, now)} · ${took(j.startedAt, j.endedAt, now)}`"
            :badge="j.state"
            :tone="jobTone(j.state)"
            @activate="router.push(`/jobs/${encodeURIComponent(j.id)}`)"
          />
        </HList>
        <template #footer><HButton variant="ghost" icon="right" label="All jobs" @click="router.push('/jobs')" /></template>
      </HCard>

      <HCard title="Job duration" description="Seconds, oldest to newest.">
        <HSparkline v-if="durations.length > 1" :values="durations" label="Job duration, seconds" :description="`${durations.length} finished jobs`" tone="accent" :width="320" :height="64" />
        <HText v-else tone="muted">Not enough finished jobs.</HText>
      </HCard>
    </HGrid>
  </HStack>
</template>
