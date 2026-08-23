<script setup lang="ts">
/**
 * The job states, and only these. Kept in one component so the colour of `leaked` can never drift
 * from the colour of `failed` in one view and not another:
 *
 *   ok  green · failed  red · leaked  AMBER · running  blue, pulsing · cancelled  grey
 *
 * `cancelled` is deliberately quiet: someone meant to stop it, so it is not an alarm — but it is
 * not `ok` either, because whatever the job had already done was left exactly as it was.
 *
 * `leaked` must be unmistakably distinct from `failed`. They are different problems with different
 * owners — "teardown errored" versus "teardown ran and something survived" — which is why the CLI
 * gives leaked its own exit code, and why this app never renders them in the same colour.
 */
import type { JobState } from '../api/types';
import { sentence } from '../composables/useFormat';

defineProps<{ state: JobState }>();
</script>

<template>
  <span class="badge" :class="state">
    <span class="dot" />
    {{ sentence(state) }}
  </span>
</template>
