<script setup lang="ts">
/** A server message shown whole, newlines intact. */
import { HAlert } from '@hearth-ui/vue';

defineProps<{ title: string; text: string }>();
</script>

<template>
  <HAlert tone="danger" :title="title">
    <pre class="verbatim">{{ text }}</pre>
  </HAlert>
</template>

<style scoped>
.verbatim {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: var(--h-font-mono, monospace);
  font-size: var(--h-font-size-sm, 0.875rem);
}
</style>
