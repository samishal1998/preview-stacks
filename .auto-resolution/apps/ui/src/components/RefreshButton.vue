<script setup lang="ts">
/**
 * Re-read what is on screen, without reloading the page.
 *
 * Every list here is a snapshot of something that changes on its own — containers come and go, jobs
 * finish, a teammate submits a deployment. Some views poll and some deliberately do not, and either
 * way the only way to see "now" used to be ⌘R: a full reload that re-fetches every panel, re-runs
 * the auth check and loses scroll position, to refresh one table.
 *
 * SPINS WHILE IT WORKS, and only then. A refresh that gives no feedback gets pressed three times;
 * one that pretends to work is worse. The icon turns and the label says so until the promise
 * settles, and the button stays disabled meanwhile so a double-click cannot start two reads.
 *
 * `label` exists because "Refresh" is not always the honest word — the deployment detail says
 * "Re-resolve", since it re-runs the variable substitution rather than just re-reading a row.
 */
import { ref } from 'vue';

const props = defineProps<{
  /** Awaited: the spin lasts exactly as long as the work does. */
  run: () => unknown | Promise<unknown>;
  label?: string;
  /** The parent is already loading for its own reasons (first load, a poll in flight). */
  busy?: boolean;
  title?: string;
}>();

const spinning = ref(false);

async function go(): Promise<void> {
  if (spinning.value) return;
  spinning.value = true;
  try {
    await props.run();
  } finally {
    spinning.value = false;
  }
}
</script>

<template>
  <button
    class="ghost sm refresh"
    :disabled="spinning || busy"
    :title="title ?? 'Read this again from the server'"
    :aria-busy="spinning ? 'true' : undefined"
    @click="go"
  >
    <span class="refresh-icon" :class="{ spin: spinning || busy }" aria-hidden="true">↻</span>
    {{ spinning ? 'Refreshing…' : (label ?? 'Refresh') }}
  </button>
</template>
