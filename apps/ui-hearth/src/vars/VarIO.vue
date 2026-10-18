<script setup lang="ts">
/**
 * Copy a variable list as .env, CSV or TSV; paste one back. Nothing is applied before it is shown:
 * the paste is parsed live and unreadable lines are listed, never dropped.
 *
 * Props: pairs (what Copy exports), note (# comment lines on .env), replace (offer "Replace all"),
 * skipEmpty (drop pasted entries with no value), busy.
 * Emits: apply(pairs, 'replace' | 'merge').
 */
import { computed, ref } from 'vue';
import { HAlert, HButton, HSelect, HStack, HText, HTextarea } from '@hearth-ui/vue';
import { FORMAT_LABEL, VAR_FORMATS, formatVars, parseVars, type VarFormat, type VarPair } from '../lib/varformats';
import { toast } from '../state';

const props = defineProps<{ pairs: VarPair[]; note?: string; replace?: boolean; skipEmpty?: boolean; busy?: boolean }>();
const emit = defineEmits<{ apply: [pairs: VarPair[], mode: 'replace' | 'merge'] }>();

const open = ref(false);
const text = ref('');
const fmt = ref('auto');
const fallback = ref('');
const PREVIEW = 12;

const exportable = computed(() => props.pairs.filter((e) => e.k).length);
const parsed = computed(() => (text.value.trim() ? parseVars(text.value, fmt.value === 'auto' ? undefined : (fmt.value as VarFormat)) : null));
const skipped = computed(() => (props.skipEmpty && parsed.value ? parsed.value.pairs.filter((e) => !e.v) : []));
const usable = computed(() => (parsed.value ? (props.skipEmpty ? parsed.value.pairs.filter((e) => e.v) : parsed.value.pairs) : []));
const formatOptions = [
  { value: 'auto', label: 'Detect format' },
  ...VAR_FORMATS.map((f) => ({ value: f, label: `Read as ${FORMAT_LABEL[f]}` })),
];

async function copy(f: VarFormat): Promise<void> {
  const out = formatVars(props.pairs, f, props.note);
  try {
    await navigator.clipboard.writeText(out);
    fallback.value = '';
    toast(`Copied ${exportable.value} as ${FORMAT_LABEL[f]}`, 'success');
  } catch {
    // No clipboard on plain http. Reveal the text instead.
    fallback.value = out;
    toast('Clipboard unavailable', 'warning', 'Copy the text below.');
  }
}

function apply(mode: 'replace' | 'merge'): void {
  emit('apply', usable.value, mode);
  text.value = '';
  open.value = false;
}
</script>

<template>
  <HStack direction="column" :gap="3">
    <HStack direction="row" :gap="2" wrap align="center">
      <HText tone="muted" size="sm">Copy as</HText>
      <HButton v-for="f in VAR_FORMATS" :key="f" size="compact" variant="ghost" :disabled="!exportable" :label="`Copy as ${FORMAT_LABEL[f]}`" @click="copy(f)">
        {{ FORMAT_LABEL[f] }}
      </HButton>
      <HButton size="compact" :aria-expanded="open" @click="open = !open">{{ open ? 'Close' : 'Paste to fill' }}</HButton>
    </HStack>

    <HStack v-if="fallback" direction="column" :gap="2">
      <HTextarea label="Exported variables" :model-value="fallback" readonly :rows="6" />
      <div><HButton size="compact" variant="ghost" @click="fallback = ''">Hide</HButton></div>
    </HStack>

    <HStack v-if="open" direction="column" :gap="3">
      <slot />
      <HTextarea v-model="text" label="Paste variables" :rows="7" placeholder="PR=7&#10;REGION=eu&#10;or two columns of CSV or TSV" />
      <HStack direction="row" :gap="3" wrap align="end">
        <HSelect v-model="fmt" label="Format" :options="formatOptions" />
        <HText v-if="parsed" tone="muted" size="sm">Read as {{ FORMAT_LABEL[parsed.format] }}</HText>
      </HStack>

      <template v-if="parsed">
        <HText size="sm">
          {{ usable.length }} variable{{ usable.length === 1 ? '' : 's' }} read.
          <template v-if="skipped.length">{{ skipped.length }} without a value skipped: {{ skipped.map((e) => e.k).join(', ') }}.</template>
        </HText>
        <ul v-if="usable.length" class="preview">
          <li v-for="(e, i) in usable.slice(0, PREVIEW)" :key="i"><code>{{ e.k }}</code> <span>{{ e.v }}</span></li>
          <li v-if="usable.length > PREVIEW">and {{ usable.length - PREVIEW }} more</li>
        </ul>

        <HAlert v-if="parsed.problems.length" tone="warning" :title="`${parsed.problems.length} line${parsed.problems.length === 1 ? '' : 's'} not read`">
          <ul class="preview">
            <li v-for="p in parsed.problems" :key="p.line">line {{ p.line }} <code>{{ p.text }}</code> {{ p.reason }}</li>
          </ul>
          Not imported.
        </HAlert>

        <HStack direction="row" :gap="2" wrap>
          <HButton v-if="replace" variant="primary" :disabled="!usable.length || busy" @click="apply('replace')">Replace all</HButton>
          <HButton :variant="replace ? 'secondary' : 'primary'" :disabled="!usable.length || busy" @click="apply('merge')">Merge</HButton>
          <HButton variant="ghost" @click="open = false">Cancel</HButton>
        </HStack>
      </template>
    </HStack>
  </HStack>
</template>

<style scoped>
.preview {
  margin: 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: var(--h-space);
  font-size: var(--h-font-size-sm, 0.875rem);
}
.preview li {
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
</style>
