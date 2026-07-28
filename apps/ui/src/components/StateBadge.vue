<script setup lang="ts">
/**
 * The four job states, and only the four. Kept in one component so the colour of `leaked` can
 * never drift from the colour of `failed` in one view and not another:
 *
 *   ok      green   ·  failed  red   ·  leaked  AMBER  ·  running  blue, pulsing
 *
 * `leaked` must be unmistakably distinct from `failed`. They are different problems with different
 * owners — "teardown errored" versus "teardown ran and something survived" — which is why the CLI
 * gives leaked its own exit code, and why this app never renders them in the same colour.
 */
import type { JobState } from '../api/types';

defineProps<{ state: JobState }>();
</script>

<template>
  <span class="badge" :class="state">
    <span class="dot" />
    {{ state }}
  </span>
</template>
