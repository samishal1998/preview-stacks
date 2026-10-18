<script setup lang="ts">
/**
 * One job: state, the log, and what each step did.
 *
 * The log streams over EventSource. The server replays the whole buffer and then goes live, so
 * attaching late still shows the start; a final `{done:true}` ends it and the job is read once more
 * for `outcome`, which only exists after it finishes. A queued job's stream is open and silent
 * until it dispatches, and its first line is that dispatch, so that line triggers one re-read.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HEmptyState,
  HLogViewer,
  HProgress,
  HResourceDetail,
  HSkeleton,
  HStack,
  HText,
  HTimeline,
  type TimelineItem,
} from '@hearth-ui/vue';
import type { Job } from '@samyx/preview-stacks-client';
import { api } from '../api';
import { coreApi } from '../api-core';
import { actionLabel, jobTone, stamp, took } from '../format';
import { addLine, countUnverifiable, isTerminal, jobTarget, leakedAxes, logEntries, stepLine, stepState, supersededBy, waitReason, waitText, type LogLine } from '../lib/jobs';
import { problem } from '../lib/problem';
import { useNow } from '../lib/now';
import { can } from '../lib/users';
import { auth, data, toast } from '../state';

const props = defineProps<{ id: string }>();
const router = useRouter();
const now = useNow(1000);

const job = ref<Job | null>(null);
const lines = ref<LogLine[]>([]);
const missing = ref(false);
const error = ref('');
const streaming = ref(false);
let source: EventSource | null = null;

const terminal = computed(() => !!job.value && isTerminal(job.value.state));
const queued = computed(() => job.value?.state === 'queued');
const wait = computed(() => (job.value ? waitReason(job.value, data.jobs) : null));
const replacement = computed(() => (job.value ? supersededBy(job.value, data.jobs) : null));
const steps = computed(() => job.value?.outcome?.steps ?? []);
const leaks = computed(() => leakedAxes(steps.value));
const unverifiable = computed(() => countUnverifiable(steps.value));
const target = computed(() => (job.value ? jobTarget(job.value) : null));
const entries = computed(() => logEntries(lines.value));

const stepTone = { ok: 'success', unverifiable: 'warning', leaked: 'danger', failed: 'danger' } as const;
const stepItems = computed<TimelineItem[]>(() =>
  steps.value.map((s, i) => ({
    id: String(i),
    title: `${s.axis} · ${s.phase.replace('_', ' ')}`,
    description: stepLine(s),
    tone: stepTone[stepState(s)],
  })),
);

async function load(): Promise<Job | null> {
  const want = props.id;
  const r = await api.job(want);
  if (want !== props.id) return null; // navigated to another job meanwhile
  if (r.status === 404) {
    missing.value = true;
    return null;
  }
  if (!r.ok) {
    error.value = problem(r, 'Load job');
    return null;
  }
  error.value = '';
  job.value = r.body.job;
  if (r.body.job.log?.length && !lines.value.length) lines.value = [...r.body.job.log];
  return r.body.job;
}

function closeStream(): void {
  source?.close();
  source = null;
  streaming.value = false;
}

function stream(): void {
  closeStream();
  lines.value = []; // the stream replays the whole buffer, so the seed would show twice
  let awaitingDispatch = queued.value;
  source = new EventSource(coreApi.streamUrl(props.id));
  streaming.value = true;
  source.onmessage = (ev) => {
    let p: unknown;
    try {
      p = JSON.parse(ev.data as string);
    } catch {
      return;
    }
    if ((p as { done?: boolean }).done) {
      closeStream();
      void load();
      return;
    }
    if (awaitingDispatch) {
      awaitingDispatch = false;
      void load();
    }
    lines.value = addLine(lines.value, p as LogLine);
  };
  // Usually the job ended between the read and the connect. Stop rather than reconnect forever.
  source.onerror = () => {
    closeStream();
    void load();
  };
}

async function init(): Promise<void> {
  const want = props.id;
  const loaded = await load();
  if (loaded && want === props.id && !isTerminal(loaded.state)) stream();
}
watch(
  () => props.id,
  () => {
    closeStream();
    lines.value = [];
    job.value = null;
    missing.value = false;
    error.value = '';
    void init();
  },
  { immediate: true },
);
onBeforeUnmount(closeStream);

// ── acting ──────────────────────────────────────────────────────────────────────────────────────
const confirmStop = ref(false);
const stopping = ref(false);
async function stop(): Promise<void> {
  confirmStop.value = false;
  stopping.value = true;
  const r = await coreApi.cancelJob(props.id);
  stopping.value = false;
  // 409: it finished on its own between the click and the request.
  if (!r.ok) toast(r.status === 409 ? 'Already finished' : problem(r, 'Stop job'), r.status === 409 ? 'info' : 'danger');
  else toast(r.body.warning ?? 'Stopped.', 'success');
  void load();
}

const rerunnable = computed(() => can('developer', auth) && terminal.value && !!job.value?.deployment && (job.value.action === 'up' || job.value.action === 'verify'));
const rerunning = ref(false);
async function rerun(): Promise<void> {
  if (!job.value?.deployment) return;
  rerunning.value = true;
  const r = await api.act(job.value.deployment, job.value.action as 'up' | 'verify');
  rerunning.value = false;
  if (r.status === 202 && r.body.job) void router.push(`/jobs/${encodeURIComponent(r.body.job.id)}`);
  else toast(problem(r, actionLabel(job.value.action)), 'danger');
}
</script>

<template>
  <HEmptyState v-if="missing" :title="`No job ${id}`" icon="search" description="The history is held in memory. A restart clears it.">
    <HButton label="Jobs" @click="router.push('/jobs')" />
  </HEmptyState>
  <HAlert v-else-if="error && !job" tone="danger" title="Could not load the job" :description="error">
    <HButton size="compact" variant="secondary" label="Retry" @click="init" />
  </HAlert>
  <HSkeleton v-else-if="!job" variant="rectangle" />

  <HResourceDetail
    v-else
    :title="actionLabel(job.action)"
    :description="job.stack"
    :breadcrumbs="[{ id: 'jobs', label: 'Jobs' }, { id: 'self', label: id }]"
    :status="job.state"
    :tone="jobTone(job.state)"
    :fields="[
      { key: 'started', label: 'Started', value: job.startedAt ? stamp(job.startedAt) : queued ? 'Not started' : 'Never started' },
      { key: 'took', label: 'Took', value: took(job.startedAt, job.endedAt, now) },
      { key: 'deployment', label: 'Deployment', value: job.deployment ?? 'Control plane' },
    ]"
    @navigate="router.push('/jobs')"
  >
    <template #actions>
      <HBadge v-if="streaming" dot tone="info" label="live" />
      <HButton v-if="!streaming && !terminal" variant="secondary" label="Reconnect" @click="stream" />
      <HButton v-if="target" variant="secondary" :label="job.deployment ? 'Deployment' : 'Control'" icon="launch" @click="router.push(target)" />
      <HButton v-if="rerunnable" variant="secondary" icon="arrow" label="Run again" :loading="rerunning" @click="rerun" />
      <HButton v-if="!terminal && can('developer', auth)" variant="danger" :label="queued ? 'Drop' : 'Stop'" :loading="stopping" @click="confirmStop = true" />
    </template>

    <HStack direction="column" :gap="4">
      <HAlert v-if="error" tone="danger" title="Could not refresh the job" :description="error" compact />
      <HProgress v-if="job.state === 'running'" label="Running" />

      <HAlert v-if="queued" tone="info" title="Waiting to start" :description="wait ? waitText(wait) : ''">
        <HButton v-if="wait?.kind === 'stack'" size="compact" variant="secondary" :label="`Open ${actionLabel(wait.blocker.action)}`" @click="router.push(`/jobs/${encodeURIComponent(wait.blocker.id)}`)" />
      </HAlert>
      <HAlert v-if="job.state === 'superseded'" tone="neutral" title="Superseded. It never ran." :description="`Replaced while queued for ${job.stack}.`">
        <HButton v-if="replacement" size="compact" variant="secondary" :label="`Open ${actionLabel(replacement.action).toLowerCase()}`" @click="router.push(`/jobs/${encodeURIComponent(replacement.id)}`)" />
      </HAlert>
      <HAlert v-if="job.state === 'cancelled'" tone="warning" :title="`Stopped by ${job.cancelledBy ?? 'an operator'}. Nothing was undone.`" description="Run Verify to see what exists." />
      <HAlert v-if="leaks.length" tone="danger" :title="`Leaked: ${leaks.join(', ')}`" description="Still present and not retried. Remove by hand." />
      <HAlert v-if="job.error" tone="danger" title="The job crashed" :description="job.error" />

      <HLogViewer v-if="entries.length" :entries="entries" label="Job log" :max-lines="5000" follow />
      <HText v-else tone="muted">{{ queued ? 'Waiting to start.' : streaming ? 'Waiting for output.' : 'No output.' }}</HText>

      <template v-if="job.outcome">
        <HText weight="bold">Steps<template v-if="unverifiable"> · {{ unverifiable }} unverifiable</template></HText>
        <HTimeline v-if="stepItems.length" :items="stepItems" label="Steps" />
        <HEmptyState v-else title="No steps recorded" icon="clock" />
      </template>
    </HStack>

    <HAlertDialog
      v-model:open="confirmStop"
      danger
      :title="queued ? 'Drop this job?' : 'Stop this job?'"
      :description="queued ? 'It never ran.' : 'Nothing is undone.'"
      :confirm-label="queued ? 'Drop' : 'Stop'"
      :busy="stopping"
      @confirm="stop"
    />
  </HResourceDetail>
</template>
