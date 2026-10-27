<script setup lang="ts">
/** HTextarea in a monospace face, for spec and compose source. Props and v-model pass through. */
import { HTextarea } from '@hearth-ui/vue';

defineProps<{ label: string }>();
</script>

<template>
  <!-- wrap="off": a soft-wrapped YAML line continues at column 0 and reads as a wrongly indented one. -->
  <HTextarea class="mono" :label="label" wrap="off" />
</template>

<style scoped>
/* HTextarea puts the class on the <textarea> itself, not on an ancestor. */
:deep(textarea.mono) {
  font-family: var(--h-font-mono, monospace);
}
</style>
