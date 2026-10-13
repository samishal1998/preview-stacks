<script setup lang="ts">
/** One job: its state, the steps it ran, and its log — polled while it is still moving. */
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlert,
  HBadge,
  HButton,
  HEmptyState,
  HErrorPage,
  HLogViewer,
  HProgress,
  HResourceDetail,
  HSkeleton,
  HStack,
  HStepper,
  type LogEntry,
  type StepItem,
} from '@hearth-ui/vue';
import type { Job } from '@samyx/preview-stacks-client';
import { api } from '../api';
import { ACTION_LABEL, jobTone, took } from '../format';
import { toast } from '../state';

const props = defineProps<{ id: string }>();
const router = useRouter();

const job = ref<Job | null>(null);
const missing = ref(false);
const live = computed(() => job.value?.state === 'queued' || job.value?.state === 'running');

async function load(): Promise<void> {
  const r = await api.job(props.id);
  if (r.status === 404) missing.value = true;
  else if (r.ok) job.value = r.body.job;
}
let timer: ReturnType<typeof setInterval> | undefined;
watch(
  () => props.id,
  () => {
    job.value = null;
    missing.value = false;
    void load();
    if (timer) clearInterval(timer);
    timer = setInterval(() => live.value !== false && void load(), 1500);
  },
  { immediate: true },
);
onBeforeUnmount(() => timer && clearInterval(timer));

const steps = computed<StepItem[]>(() =>
  (job.value?.outcome?.steps ?? []).map((s, i) => ({
    id: String(i),
    label: `${s.phase.replace('_', ' ')} · ${s.axis}`,
    description: s.skipped ? 'skipped' : s.ok ? 'ok' : (s.message ?? `exit ${s.code}`),
    completed: s.ok && !s.skipped,
  })),
);
const entries = computed<LogEntry[]>(() =>
  (job.value?.log ?? []).map((l) => ({
    id: String(l.seq),
    message: l.message,
    timestamp: new Date(l.at).toLocaleTimeString(),
    level: l.level === 'error' ? 'error' : l.level === 'warn' ? 'warning' : 'info',
  })),
);

async function cancel(): Promise<void> {
  if (!job.value?.deployment) return;
  const r = await api.cancel(job.value.deployment);
  toast(r.ok ? 'Cancelled' : 'Could not cancel', r.ok ? 'success' : 'danger', r.ok ? undefined : r.body.error);
  void load();
}
</script>

<template>
  <HErrorPage v-if="missing" kind="not-found" brand="pstack" home-href="/jobs" :title="`No job ${id}`" description="The history is held in memory; a restart clears it." />
  <HSkeleton v-else-if="!job" variant="rectangle" />

  <HResourceDetail
    v-else
    :title="ACTION_LABEL[job.action] ?? job.action"
    :description="job.stack"
    :breadcrumbs="[{ id: 'jobs', label: 'Jobs' }, { id: 'self', label: id }]"
    :status="job.state"
    :tone="jobTone(job.state)"
    :fields="[
      { key: 'stack', label: 'Stack', value: job.stack },
      { key: 'deployment', label: 'Deployment', value: job.deployment ?? 'control plane', href: job.deployment ? `/deployments/${encodeURIComponent(job.deployment)}` : undefined },
      { key: 'took', label: 'Took', value: took(job.startedAt, job.endedAt) },
    ]"
    @navigate="router.push('/jobs')"
  >
    <template #actions>
      <HButton v-if="live && job.deployment" variant="danger" label="Cancel" @click="cancel" />
      <HBadge v-if="live" dot tone="info" label="live" />
    </template>

    <HStack direction="column" :gap="4">
      <HProgress v-if="live" label="Running" />
      <HAlert v-if="job.error" tone="danger" title="The job crashed" :description="job.error" />
      <HAlert v-if="job.state === 'leaked'" tone="danger" title="Something was left behind" description="A teardown ran and a resource still exists. It will not be retried." />
      <HStepper v-if="steps.length" :items="steps" label="Steps" orientation="vertical" />
      <HEmptyState v-else-if="!live" title="No steps recorded" icon="clock" />
      <HLogViewer :entries="entries" label="Job log" follow />
    </HStack>
  </HResourceDetail>
</template>
