<script setup lang="ts">
/** Preconditions, checked in order before anything is created. `up` stops at the first failure. */
import { computed, watch } from 'vue';
import { HAlert, HButton, HCard, HDataTable, HSkeleton, HStack, type TableColumn } from '@hearth-ui/vue';
import type { DeploymentRow, Runtime } from '@samyx/preview-stacks-client';
import { detail, openDetail, reloadDetail } from '../../vars/useDetail';

const props = defineProps<{ id: string; dep: DeploymentRow | null; runtime: Runtime | null }>();
watch(() => props.id, openDetail, { immediate: true });

const columns: TableColumn[] = [
  { key: 'name', label: 'Requirement' },
  { key: 'hint', label: 'Hint on failure' },
];
const rows = computed(() => (detail.data?.requires ?? []).map((r) => ({ id: r.name, name: r.name, hint: r.hint || 'No hint' })));
</script>

<template>
  <HCard title="Requires" description="Checked before anything is created.">
    <template #footer>
      <HButton variant="secondary" icon="arrow" label="Re-resolve" :loading="detail.loading" @click="reloadDetail" />
    </template>
    <HSkeleton v-if="detail.loading && !detail.data && !detail.error" variant="text" :lines="3" label="Loading requirements" />
    <HAlert v-else-if="detail.error" tone="danger" title="Spec did not resolve" :description="detail.error" />
    <HStack v-else direction="column" :gap="3">
      <HDataTable :rows="rows" :columns="columns" label="Requirements" empty-text="None declared." />
    </HStack>
  </HCard>
</template>
