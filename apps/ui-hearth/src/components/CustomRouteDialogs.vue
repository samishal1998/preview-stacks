<script setup lang="ts">
/** The move, unassign and delete confirms for one `useCustomRoutes()`. Both pages that write routes draw these. */
import { HAlertDialog } from '@hearth-ui/vue';
import type { useCustomRoutes } from '../lib/use-custom-routes';

const props = defineProps<{ routes: ReturnType<typeof useCustomRoutes> }>();
const r = props.routes;
const { moveOpen, dropOpen } = r;
</script>

<template>
  <HAlertDialog v-model:open="moveOpen" :title="r.move.value?.question ?? ''" confirm-label="Move" :busy="r.busy.value" @confirm="r.confirmMove" />
  <HAlertDialog
    v-model:open="dropOpen"
    :danger="r.drop.value?.kind === 'delete'"
    :title="r.drop.value ? `${r.drop.value.kind === 'delete' ? 'Delete' : 'Unassign'} ${r.drop.value.name}?` : ''"
    :description="r.drop.value?.kind === 'delete' ? 'Its hosts stop answering and the name is freed.' : 'Its hosts stop answering.'"
    :confirm-label="r.drop.value?.kind === 'delete' ? 'Delete' : 'Unassign'"
    :busy="r.busy.value"
    @confirm="r.confirmDrop"
  />
</template>
