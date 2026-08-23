<script setup lang="ts">
/**
 * A loading placeholder shaped like the thing that is coming.
 *
 * Deliberately not a spinner on an empty page: a spinner says "wait" and nothing else, while a
 * skeleton says what will be there and stops the layout jumping when it arrives. It also makes an
 * empty result visibly different from a slow one, which on this page is the difference between
 * "nothing is deployed" and "the API is not answering".
 */
withDefaults(defineProps<{ rows?: number; tall?: boolean }>(), { rows: 3, tall: false });
</script>

<template>
  <div role="status" aria-live="polite" aria-label="loading">
    <div v-for="n in rows" :key="n" class="skel" :class="{ tall }" :style="{ width: `${100 - n * 7}%` }" />
  </div>
</template>
