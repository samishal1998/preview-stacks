<script setup lang="ts">
/** The host at a glance: what is running, what failed, and what just happened. */
import { computed } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlert,
  HButton,
  HCard,
  HEmptyState,
  HGrid,
  HPageHeader,
  HSkeleton,
  HSparkline,
  HStatCard,
  HStack,
  HText,
  HTimeline,
  type TimelineItem,
} from '@hearth-ui/vue';
import { data, summary } from '../state';
import { ACTION_LABEL, ago, jobTone, took } from '../format';

const router = useRouter();

const recent = computed<TimelineItem[]>(() =>
  data.jobs.slice(0, 8).map((j) => ({
    id: j.id,
    title: `${ACTION_LABEL[j.action] ?? j.action} · ${j.stack}`,
    description: `${j.state}${j.endedAt ? ` in ${took(j.startedAt, j.endedAt)}` : ''}`,
    timestamp: ago(j.startedAt),
    dateTime: j.startedAt ? new Date(j.startedAt).toISOString() : undefined,
    tone: jobTone(j.state),
  })),
);

/** Wall-clock seconds of the finished jobs, oldest first, so the line reads left to right. */
const durations = computed(() =>
  data.jobs
    .filter((j) => j.startedAt && j.endedAt)
    .slice(0, 20)
    .reverse()
    .map((j) => Math.round((j.endedAt! - j.startedAt!) / 1000)),
);
const attention = computed(() => data.deployments.filter((d) => d.unresolved));
</script>

<template>
  <HStack direction="column" :gap="6">
    <HPageHeader
      variant="hero"
      title="This host,"
      accent="at a glance."
      description="Previews running here, what is in flight, and what went wrong."
    />

    <HSkeleton v-if="!data.loaded" variant="rectangle" />
    <template v-else>
      <HGrid min-column-width="12rem" :gap="4">
        <HStatCard label="Deployments" :value="summary.total" icon="apps" :detail="`${summary.running} running`" />
        <HStatCard label="Busy" :value="summary.busy" icon="clock" tone="info" detail="a job is in flight" />
        <HStatCard label="Asleep" :value="summary.asleep" icon="moon" tone="neutral" detail="wake on request" />
        <HStatCard
          label="Failed jobs"
          :value="summary.failedJobs"
          icon="health"
          :tone="summary.failedJobs ? 'danger' : 'success'"
          detail="of the last 50"
        />
      </HGrid>

      <HAlert
        v-for="d in attention"
        :key="d.id"
        tone="warning"
        :title="`${d.id} cannot be resolved`"
        :description="d.unresolved"
      />

      <HGrid min-column-width="22rem" :gap="4">
        <HCard title="Recent jobs" description="Newest first.">
          <HEmptyState v-if="!recent.length" title="No jobs yet" description="Deploy a preview and it shows up here." icon="clock" />
          <HTimeline v-else :items="recent" compact label="Recent jobs" />
        </HCard>

        <HCard title="How long jobs take" description="Seconds, oldest to newest.">
          <HSparkline
            v-if="durations.length > 1"
            :values="durations"
            label="Job duration, seconds"
            :description="`${durations.length} finished jobs`"
            tone="accent"
            :width="320"
            :height="64"
          />
          <HText v-else tone="muted">Not enough finished jobs yet.</HText>
        </HCard>
      </HGrid>
      <HButton variant="ghost" icon="right" label="All jobs" @click="router.push('/jobs')" />
    </template>
  </HStack>
</template>
