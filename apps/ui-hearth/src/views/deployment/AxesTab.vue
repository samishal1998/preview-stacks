<script setup lang="ts">
/**
 * Axes and their hooks. An axis with no `assert_gone` cannot be proven torn down: a third state,
 * neither pass nor fail, never shown as success.
 */
import { computed, watch } from 'vue';
import { HAlert, HBadge, HButton, HCard, HDataTable, HSkeleton, HStack, type TableColumn } from '@hearth-ui/vue';
import type { DeploymentRow, Runtime } from '@samyx/preview-stacks-client';
import type { HookName } from '../../api-vars';
import { detail, openDetail, reloadDetail, unverifiableAxes } from '../../vars/useDetail';

const props = defineProps<{ id: string; dep: DeploymentRow | null; runtime: Runtime | null }>();
watch(() => props.id, openDetail, { immediate: true });

const HOOKS: HookName[] = ['up', 'assert_live', 'down', 'assert_gone'];
const columns: TableColumn[] = [
  { key: 'name', label: 'Axis', sortable: true },
  { key: 'hooks', label: 'Hooks' },
  { key: 'verifiable', label: 'Teardown provable' },
];
const rows = computed(() => (detail.data?.axes ?? []).map((a) => ({ id: a.name, name: a.name, hooks: a.hooks, verifiable: a.verifiable })));
</script>

<template>
  <HCard title="Axes">
    <template #footer>
      <HButton variant="secondary" icon="arrow" label="Re-resolve" :loading="detail.loading" @click="reloadDetail" />
    </template>
    <HSkeleton v-if="detail.loading && !detail.data && !detail.error" variant="text" :lines="3" label="Loading axes" />
    <HAlert v-else-if="detail.error" tone="danger" title="Spec did not resolve" :description="detail.error" />
    <HStack v-else direction="column" :gap="3">
      <HDataTable :rows="rows" :columns="columns" label="Axes" empty-text="No axes.">
        <template #cell="{ row, column, value }">
          <HStack direction="row" v-if="column.key === 'hooks'" :gap="1" wrap>
            <HBadge v-for="h in HOOKS" :key="h" size="sm" :label="(row.hooks as string[]).includes(h) ? h : `${h} (none)`" :variant="(row.hooks as string[]).includes(h) ? 'soft' : 'dashed'" />
          </HStack>
          <HBadge v-else-if="column.key === 'verifiable'" :tone="value ? 'success' : 'warning'" :label="value ? 'assert_gone' : 'Unverifiable'" />
          <template v-else>{{ value }}</template>
        </template>
      </HDataTable>
      <HAlert
        v-if="unverifiableAxes.length"
        tone="warning"
        :title="`${unverifiableAxes.length === 1 ? '1 axis cannot' : `${unverifiableAxes.length} axes cannot`} be verified: ${unverifiableAxes.join(', ')}`"
      />
    </HStack>
  </HCard>
</template>
