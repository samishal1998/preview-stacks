<script setup lang="ts">
/**
 * A 409, explained. It always means refused, nothing started; the cause differs and the fix with it.
 * The server's own message is printed verbatim and nothing is retried.
 */
import { computed } from 'vue';
import { useRouter } from 'vue-router';
import { HAlert, HButton } from '@hearth-ui/vue';
import type { Conflict } from '../../lib/deployment';

const props = defineProps<{ conflict: Conflict; jobId?: string }>();
const router = useRouter();

const hint = computed(() => {
  const c = props.conflict;
  if (c.kind === 'shared') return 'Type the stack name and tear down again.';
  if (c.kind === 'containers') return 'Tear down first. Forgetting now would orphan those containers.';
  if (c.kind === 'referenced') return `Still used by ${c.deployments.join(', ')}.`;
  return props.jobId ? '' : 'Nothing was retried. Wait for the current work to finish.';
});
</script>

<template>
  <HAlert :tone="conflict.kind === 'shared' ? 'warning' : 'info'" title="Refused. Nothing was started." :description="[conflict.text, hint].filter(Boolean).join(' ')">
    <HButton v-if="jobId" variant="ghost" label="Follow the running job" trailing-icon="right" @click="router.push(`/jobs/${encodeURIComponent(jobId)}`)" />
  </HAlert>
</template>
