<script setup lang="ts">
/** One custom route's Subdomains cell: a select that stages, and Save. Viewers get the value as text. */
import { nextTick, ref } from 'vue';
import { HButton, HSelect, HStack, HText } from '@hearth-ui/vue';
import type { CustomRoute } from '../api-routing';
import { DEPTHS, depthLabel, focusLost } from '../lib/customRoutes';
import type { useCustomRoutes } from '../lib/use-custom-routes';

const props = defineProps<{ routes: ReturnType<typeof useCustomRoutes>; route: CustomRoute }>();
const cr = props.routes;
const box = ref<HTMLElement | null>(null);

// Save goes once its pick lands: land on the select, not <body>.
async function save(): Promise<void> {
  await cr.saveSubdomains(props.route.name);
  await nextTick();
  if (focusLost(document.activeElement, document.body)) box.value?.querySelector('select')?.focus();
}
</script>

<template>
  <div v-if="cr.mayWrite.value" ref="box">
    <HStack direction="row" :gap="2" wrap align="center">
      <HSelect
        :label="`Subdomains for ${route.name}`"
        hide-label
        size="compact"
        :model-value="cr.subOf(route)"
        :options="DEPTHS"
        @update:model-value="(v: string) => (cr.subPick[route.name] = v)"
      />
      <HButton
        v-if="cr.subStaged(route)"
        variant="secondary"
        size="compact"
        label="Save"
        :aria-label="`Save subdomains for ${route.name}`"
        :loading="cr.subBusy.value === route.name"
        @click="save"
      />
    </HStack>
  </div>
  <HText v-else>{{ depthLabel(route.subdomains) }}</HText>
</template>
