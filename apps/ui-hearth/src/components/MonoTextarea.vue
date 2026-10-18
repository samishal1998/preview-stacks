<script setup lang="ts">
/** HTextarea in a monospace face, for spec and compose source. Props and v-model pass through. */
import { HTextarea } from '@hearth-ui/vue';

defineProps<{ label: string }>();
</script>

<template>
  <HTextarea class="mono" :label="label" />
</template>

<style scoped>
.mono :deep(textarea) {
  font-family: var(--h-font-mono, monospace);
}
</style>
