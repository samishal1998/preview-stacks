<script setup lang="ts">
/**
 * A deployment's live state, from the two TRI-STATE fields the server sends.
 *
 * `busy` and `running` are `boolean | null`, and `null` means "the server could not determine
 * this": an unresolved spec has no stack name to look up, and a docker that did not answer is not
 * the same fact as "nothing is running". Rendering either as "idle" would be a guess presented as
 * a fact — that is how a live stack gets reported as torn down and someone forgets it.
 *
 * So unknown says unknown, and it gets its own colour.
 */
defineProps<{ busy: boolean | null; running: boolean | null }>();
</script>

<template>
  <span v-if="busy === true" class="badge busy"><span class="dot pulse" />Busy</span>
  <span v-else-if="running === true" class="badge ok"><span class="dot" />Running</span>
  <span
    v-else-if="busy === null || running === null"
    class="badge unknown"
    title="the server could not determine this — it is not the same as idle"
    >Unknown</span
  >
  <span v-else class="badge">Idle</span>
</template>
