import { computed, onBeforeUnmount, ref } from 'vue';
import type { TableColumn } from '@hearth-ui/vue';

/** HDataTable drops a hideBelow column from its card layout too, though a card has room for every
 *  field. Cards show at the table's default mobile breakpoint (a 640px viewport); there every column
 *  is kept, and hideBelow only thins the desktop table. */
export function useCardColumns(columns: TableColumn[]) {
  const mq = window.matchMedia('(max-width: 640px)');
  const cards = ref(mq.matches);
  const sync = () => (cards.value = mq.matches);
  mq.addEventListener('change', sync);
  onBeforeUnmount(() => mq.removeEventListener('change', sync));
  return computed(() => (cards.value ? columns.map(({ hideBelow: _, ...c }) => c) : columns));
}
