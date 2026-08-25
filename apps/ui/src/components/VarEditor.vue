<script setup lang="ts">
/**
 * The free-form variable editor, shared by the Config tab and Submit.
 *
 * Free-form on purpose: the spec's `env:` block lists what it DECLARES, which is not the same as
 * what it NEEDS — `stack: pr-${PR}` consumes `PR` without declaring it anywhere. The authoritative
 * list of what is missing is the server's 400 text, not this form.
 *
 * The rows are the editor; `VarIO` under them is the same list as `.env`, CSV or TSV. Typing a
 * dozen variables one input at a time is how they end up mistyped, and the list an operator
 * already has is almost always one of those three formats.
 */
import type { VarPair } from '../api/types';
import { mergeVars } from '../composables/useVarFormats';
import VarIO from './VarIO.vue';

const props = defineProps<{ modelValue: VarPair[] }>();
const emit = defineEmits<{ 'update:modelValue': [VarPair[]]; change: [] }>();

function set(i: number, patch: Partial<VarPair>): void {
  const next = props.modelValue.map((e, n) => (n === i ? { ...e, ...patch } : e));
  emit('update:modelValue', next);
  emit('change');
}

function add(): void {
  emit('update:modelValue', [...props.modelValue, { k: '', v: '' }]);
  emit('change');
}

/**
 * Both modes are honest here — this editor owns its whole list, so replacing it is a thing the
 * operator can mean. A replace that parsed to nothing still leaves one blank row: an editor with no
 * rows looks broken.
 */
function applyImport(pairs: VarPair[], mode: 'replace' | 'merge'): void {
  const next = mode === 'replace' ? pairs : mergeVars(props.modelValue, pairs);
  emit('update:modelValue', next.length ? next : [{ k: '', v: '' }]);
  emit('change');
}

function remove(i: number): void {
  const next = props.modelValue.filter((_, n) => n !== i);
  // Never leave the editor with no rows: an empty list looks like a broken control.
  emit('update:modelValue', next.length ? next : [{ k: '', v: '' }]);
  emit('change');
}
</script>

<template>
  <div>
    <div class="kv head"><span>name</span><span>value</span><span /></div>
    <div v-for="(e, i) in modelValue" :key="i" class="kv">
      <input
        type="text"
        :value="e.k"
        :aria-label="`variable name ${i + 1}`"
        placeholder="PR"
        spellcheck="false"
        autocomplete="off"
        @input="set(i, { k: ($event.target as HTMLInputElement).value.trim() })"
      />
      <input
        type="text"
        :value="e.v"
        :aria-label="`variable value ${i + 1}`"
        placeholder="7"
        spellcheck="false"
        autocomplete="off"
        @input="set(i, { v: ($event.target as HTMLInputElement).value })"
      />
      <button class="sm ghost" :aria-label="`remove variable ${i + 1}`" @click="remove(i)">−</button>
    </div>
    <button class="sm" @click="add">+ variable</button>

    <VarIO :pairs="modelValue" replace @apply="applyImport">
      <p class="hint" style="margin-top: 0">
        <b>Replace all</b> uses exactly what was pasted. <b>Merge</b> overwrites the names it finds,
        keeps the rest, and adds what is new.
      </p>
    </VarIO>
  </div>
</template>
