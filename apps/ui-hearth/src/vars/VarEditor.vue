<script setup lang="ts">
/**
 * Free-form NAME/value rows plus VarIO (copy out, paste in).
 *
 * Usage: `<VarEditor v-model="pairs" @change="save" />` where pairs is `VarPair[]` from
 * lib/varformats ({ k, v }). Emits update:modelValue on every edit and `change` after it.
 * Always keeps at least one blank row. Props: disabled. Slot `import-note` renders above the
 * paste box. Use `varsQuery(pairs)` / `recordFromPairs(pairs)` from lib/vars to send the rows.
 */
import { HButton, HInput, HStack } from '@hearth-ui/vue';
import { mergeVars, type VarPair } from '../lib/varformats';
import { validName, withBlank } from '../lib/vars';
import VarIO from './VarIO.vue';

const props = defineProps<{ modelValue: VarPair[]; disabled?: boolean }>();
const emit = defineEmits<{ 'update:modelValue': [VarPair[]]; change: [] }>();

function put(next: VarPair[]): void {
  emit('update:modelValue', withBlank(next));
  emit('change');
}
const set = (i: number, patch: Partial<VarPair>): void => put(props.modelValue.map((e, n) => (n === i ? { ...e, ...patch } : e)));
const add = (): void => put([...props.modelValue, { k: '', v: '' }]);
const remove = (i: number): void => put(props.modelValue.filter((_, n) => n !== i));
const applyImport = (pairs: VarPair[], mode: 'replace' | 'merge'): void => put(mode === 'replace' ? pairs : mergeVars(props.modelValue, pairs));
</script>

<template>
  <HStack direction="column" :gap="3">
    <HStack direction="row" v-for="(e, i) in modelValue" :key="i" :gap="2" align="start" class="row">
      <HInput
        class="grow"
        :label="`Name ${i + 1}`"
        hide-label
        placeholder="PR"
        autocomplete="off"
        :disabled="disabled"
        :model-value="e.k"
        :error="validName(e.k) ? undefined : 'Letters, digits and _ only.'"
        @update:model-value="(v: string) => set(i, { k: v.trim() })"
      />
      <HInput
        class="grow"
        :label="`Value ${i + 1}`"
        hide-label
        placeholder="7"
        autocomplete="off"
        :disabled="disabled"
        :model-value="e.v"
        @update:model-value="(v: string) => set(i, { v })"
      />
      <HButton icon-only icon="close" variant="ghost" :label="`Remove variable ${i + 1}`" :disabled="disabled" @click="remove(i)" />
    </HStack>
    <div><HButton size="compact" icon="plus" :disabled="disabled" @click="add">Variable</HButton></div>
    <VarIO :pairs="modelValue" replace :busy="disabled" @apply="applyImport">
      <slot name="import-note" />
    </VarIO>
  </HStack>
</template>

<style scoped>
.row {
  flex-wrap: nowrap;
}
.grow {
  flex: 1 1 0;
  min-width: 0;
}
</style>
