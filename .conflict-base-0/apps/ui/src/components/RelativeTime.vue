<script setup lang="ts">
/**
 * A timestamp that stays true — `2m ago` becomes `3m ago` without a reload.
 *
 * WHY NOT JUST CALL `ago()`. It is already correct at first render and wrong a minute later, and a
 * control plane is a page people leave open while watching a deploy. A relative time that silently
 * stops advancing is worse than an absolute one: it looks live and is not.
 *
 * ONE TICKER FOR THE WHOLE PAGE. A per-instance `setInterval` on a hundred-row job list is a
 * hundred timers waking the tab independently. This is a single module-level interval that bumps
 * one shared ref; every instance recomputes from it, and the interval only exists while at least
 * one instance is mounted.
 *
 * THE EXACT TIME IS NEVER LOST — it moves into `title`. "3m ago" is what you scan; the ISO stamp is
 * what you need the moment you are correlating with a log somewhere else, and throwing it away to
 * gain readability would be a bad trade.
 */
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { ago, stamp } from '../composables/useFormat';

const props = defineProps<{ at: number | undefined }>();

const now = ref(Date.now());
let timer: ReturnType<typeof setInterval> | null = null;
let mounted = 0;

/**
 * 15s, not 1s. The coarsest unit this renders is a minute, so ticking every second would repaint
 * sixty times to change a label once; 15s bounds the staleness to a quarter of the smallest unit,
 * which nobody can perceive.
 */
const TICK_MS = 15_000;

onMounted(() => {
  mounted++;
  if (!timer) timer = setInterval(() => (now.value = Date.now()), TICK_MS);
});
onUnmounted(() => {
  mounted--;
  if (mounted === 0 && timer) {
    clearInterval(timer);
    timer = null;
  }
});

// Reading `now` is what subscribes this instance to the ticker; `ago` itself calls Date.now().
const text = computed(() => (now.value, ago(props.at)));
</script>

<template>
  <time :datetime="at ? new Date(at).toISOString() : undefined" :title="stamp(at)">{{ text }}</time>
</template>
