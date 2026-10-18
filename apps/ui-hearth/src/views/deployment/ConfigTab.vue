<script setup lang="ts">
/**
 * Request variables (what we send) and declared env (what the spec declares).
 * A request variable overrides a stored one, so `up` and `down` can resolve to different stacks
 * if they disagree. The pairs are remembered per browser; other clients send their own.
 */
import { computed, watch } from 'vue';
import { HAlert, HBadge, HButton, HCard, HDataTable, HDescriptionList, HSkeleton, HStack, HText, type TableColumn } from '@hearth-ui/vue';
import type { DeploymentRow, Runtime } from '@samyx/preview-stacks-client';
import { conflictingVars } from '../../lib/vars';
import { applyVars, detail, openDetail, persistVars } from '../../vars/useDetail';
import VarEditor from '../../vars/VarEditor.vue';

const props = defineProps<{ id: string; dep: DeploymentRow | null; runtime: Runtime | null }>();
watch(() => props.id, openDetail, { immediate: true });

const stored = computed(() => props.dep?.vars);
const storedItems = computed(() => Object.entries(stored.value ?? {}).map(([k, v]) => ({ key: k, label: k, value: v })));
const clashes = computed(() => conflictingVars(detail.vars, stored.value));
const count = computed(() => detail.vars.filter((e) => e.k).length);

const columns: TableColumn[] = [
  { key: 'key', label: 'Key', sortable: true },
  { key: 'value', label: 'Value' },
  { key: 'visibility', label: 'Visibility', hideBelow: 420 },
  { key: 'length', label: 'Length (chars)', align: 'end', hideBelow: 520 },
];
const rows = computed(() => (detail.data?.env ?? []).map((e) => ({ ...e, id: e.key })));
</script>

<template>
  <HStack direction="column" :gap="4">
    <HCard title="Request variables" description="Per browser.">
      <HStack direction="column" :gap="3">
        <VarEditor v-model="detail.vars" @change="persistVars" />
        <HStack direction="row" :gap="3" align="center" wrap>
          <HButton variant="primary" :loading="detail.loading" @click="applyVars">Apply and reload</HButton>
          <HText tone="muted" size="sm">{{ count ? `${count} variable${count === 1 ? '' : 's'}` : 'No variables' }}</HText>
        </HStack>
      </HStack>
    </HCard>

    <HCard v-if="storedItems.length" title="Stored on the server" description="Request variables override these.">
      <HStack direction="column" :gap="3">
        <HDescriptionList :items="storedItems" variant="inline" dense />
        <HAlert v-if="clashes.length" tone="warning" :title="`Overridden: ${clashes.join(', ')}`" description="Stacks differ." />
      </HStack>
    </HCard>

    <HCard title="Declared env" description="After interpolation.">
      <HSkeleton v-if="detail.loading && !detail.data && !detail.error" variant="text" :lines="4" label="Loading env" />
      <HAlert v-else-if="detail.error" tone="danger" title="Spec did not resolve" :description="detail.error" />
      <HStack v-else direction="column" :gap="3">
        <HDataTable :rows="rows" :columns="columns" label="Declared env" empty-text="No env block." :page-size="25">
          <template #cell="{ row, column, value }">
            <HBadge v-if="column.key === 'visibility'" :tone="value === 'masked' ? 'warning' : 'neutral'" :label="String(value)" />
            <template v-else-if="column.key === 'length'">
              {{ value }}
              <HBadge v-if="value === 0" tone="warning" label="Never set" />
            </template>
            <code v-else-if="column.key === 'value' && row.visibility === 'masked'">{{ value }}</code>
            <span v-else-if="column.key === 'value'" class="wrap">{{ value }}</span>
            <template v-else>{{ value }}</template>
          </template>
        </HDataTable>
        <HText tone="muted" size="sm">Masked values are never sent to this page.</HText>
      </HStack>
    </HCard>
  </HStack>
</template>

<style scoped>
.wrap {
  overflow-wrap: anywhere;
}
</style>
