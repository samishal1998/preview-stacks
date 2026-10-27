<script setup lang="ts">
/** The custom routes pointed at this deployment, and assigning one: an existing route, or a new name. */
import { computed, nextTick, ref, watch } from 'vue';
import { HAlert, HButton, HCard, HDataTable, HInput, HSelect, HStack, HText, type MenuAction, type TableColumn, type TableRow } from '@hearth-ui/vue';
import CustomRouteDialogs from '../../components/CustomRouteDialogs.vue';
import { focusLost, hostItems } from '../../lib/customRoutes';
import { useCustomRoutes } from '../../lib/use-custom-routes';

const props = defineProps<{ id: string }>();
const cr = useCustomRoutes();
watch(() => props.id, () => void cr.load(), { immediate: true });

const mine = computed(() => cr.routes.value.filter((r) => r.deployment === props.id));
const columns: TableColumn[] = [
  { key: 'name', label: 'Route' },
  { key: 'hosts', label: 'Hosts' },
];
const rows = computed<TableRow[]>(() => mine.value.map((r) => ({ id: r.name, name: r.name, hosts: '' })));
const hostsOf = (row: TableRow) => hostItems(mine.value.find((r) => r.name === row.id)?.hosts ?? []);
const actions = (): MenuAction[] => [{ id: 'unassign', label: 'Unassign' }];

const NEW = '+new'; // not a DNS label, so never a route name
const pick = ref('');
const newName = ref('');
const options = computed(() => [
  ...cr.routes.value
    .filter((r) => r.deployment !== props.id)
    .map((r) => ({ value: r.name, label: r.stack || r.deployment ? `${r.name} · ${r.stack ?? r.deployment}` : r.name })),
  { value: NEW, label: 'New route' },
]);
const target = computed(() => (pick.value === NEW ? newName.value.trim() : pick.value));
async function assign(): Promise<void> {
  if (target.value) await cr.assign(target.value, props.id);
}
// The pick is done once its route shows up here: a cancelled move or a refused assign keeps it.
watch(mine, (m) => {
  if (target.value && m.some((r) => r.name === target.value)) pick.value = newName.value = '';
});
// A closed confirm returns focus to its trigger, which the write may have removed (the row) or
// disabled (Assign, once the pick clears): land on the Route select, not <body>. After the tick:
// the dialog closes in the post-flush.
const form = ref<HTMLFormElement | null>(null);
watch([() => cr.move.value, () => cr.drop.value, target], async ([m, d]) => {
  if (m || d) return;
  await nextTick();
  if (focusLost(document.activeElement, document.body)) form.value?.querySelector('select')?.focus();
});
</script>

<template>
  <HCard title="Custom routes">
    <HStack direction="column" :gap="3">
      <HAlert v-if="cr.error.value" tone="danger" :description="cr.error.value" compact />
      <HDataTable
        v-else-if="mine.length || !cr.loaded.value"
        label="Custom routes"
        :rows="rows"
        :columns="columns"
        :loading="!cr.loaded.value"
        :get-row-actions="cr.mayWrite.value ? actions : undefined"
        @row-action="cr.drop.value = { name: $event.id, kind: 'unassign' }"
      >
        <template #cell="{ row, column, value }">
          <HStack v-if="column.key === 'hosts'" direction="column" :gap="1">
            <template v-for="h in hostsOf(row)" :key="h.host">
              <a v-if="h.href" :href="h.href" target="_blank" rel="noreferrer">{{ h.host }}</a>
              <HText v-else tone="muted">{{ h.host }}</HText>
            </template>
            <template v-if="!hostsOf(row).length">—</template>
          </HStack>
          <template v-else>{{ value }}</template>
        </template>
      </HDataTable>
      <HText v-else tone="muted">None.</HText>

      <form v-if="cr.mayWrite.value" ref="form" @submit.prevent="assign">
        <HStack direction="row" :gap="3" wrap align="end">
          <HSelect v-model="pick" label="Route" placeholder="Choose a route" :options="options" />
          <HInput v-if="pick === NEW" v-model="newName" label="Name" placeholder="checkout" autocomplete="off" />
          <HButton type="submit" variant="secondary" label="Assign route" :loading="cr.busy.value" :disabled="!target" />
        </HStack>
      </form>
    </HStack>
    <CustomRouteDialogs :routes="cr" />
  </HCard>
</template>
