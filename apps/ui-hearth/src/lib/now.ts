import { onBeforeUnmount, ref } from 'vue';

/** A clock ref that ticks, so "3m ago" keeps moving while a page stays open. */
export function useNow(ms = 15000) {
  const now = ref(Date.now());
  const t = setInterval(() => (now.value = Date.now()), ms);
  onBeforeUnmount(() => clearInterval(t));
  return now;
}
