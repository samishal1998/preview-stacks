import { onBeforeUnmount, onMounted, ref } from 'vue';

/** "/" focuses the input inside the returned element ref, as in the first UI. */
export function useSlashFocus() {
  const el = ref<HTMLElement>();
  function onKey(e: KeyboardEvent): void {
    const t = e.target as HTMLElement | null;
    if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey || t?.closest('input, textarea, select, [contenteditable]')) return;
    e.preventDefault();
    el.value?.querySelector('input')?.focus();
  }
  onMounted(() => window.addEventListener('keydown', onKey));
  onBeforeUnmount(() => window.removeEventListener('keydown', onKey));
  return el;
}
