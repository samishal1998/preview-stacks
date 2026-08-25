<script setup lang="ts">
/**
 * Copy a variable list out as `.env`, CSV or TSV; paste one back in. The chrome only — every rule
 * about what those formats mean lives in `composables/useVarFormats.ts`, which is tested directly
 * because this component cannot be.
 *
 * NOTHING IS REPLACED BEFORE IT IS SHOWN. The paste box parses as you type and renders what it
 * read, including the lines it could not read, and only then offers the buttons that change
 * anything. A paste that half-worked in silence is how a deployment loses the one variable its
 * stack name is built from — which is the leak this project exists to prevent, arriving through a
 * text box.
 *
 * MERGE AND REPLACE ARE BOTH REAL ANSWERS, so the operator picks. `replace` is offered only where
 * this component's owner can actually replace the whole list: on the host-variable page a
 * "replace" would have to DELETE the rows the paste omits, and a delete that happens because
 * something was missing from a paste is not a thing to offer behind a two-word button.
 *
 * THE CLIPBOARD IS NOT ALWAYS THERE. `navigator.clipboard` is undefined on a plain-http origin —
 * exactly how this app is reached through a loopback tunnel — and a secure origin can still refuse
 * the write. A failed copy therefore reveals a selected textarea instead of only apologising in a
 * toast: the text exists either way, only the shortcut is missing.
 */
import { computed, ref } from 'vue';
import type { VarPair } from '../api/types';
import {
  FORMAT_LABEL,
  VAR_FORMATS,
  formatVars,
  parseVars,
  type VarFormat,
} from '../composables/useVarFormats';
import { toast } from '../composables/useToasts';
import SelectMenu from './SelectMenu.vue';

const props = defineProps<{
  /** What "copy" exports. Blank rows are dropped by the formatter. */
  pairs: VarPair[];
  /** `#` comment lines on the `.env` export — the withheld-secrets surface passes one. */
  note?: string;
  /** Offer "Replace all". See the header: not every owner can honestly replace. */
  replace?: boolean;
  /** Skip parsed entries with no value, and say so. For a store that refuses an empty value. */
  skipEmpty?: boolean;
  /** An import is in flight upstream. */
  busy?: boolean;
}>();

const emit = defineEmits<{ apply: [pairs: VarPair[], mode: 'replace' | 'merge'] }>();

const open = ref(false);
const text = ref('');
const fmt = ref('auto');
/** The export text, revealed only when the clipboard refused it. */
const fallback = ref('');

const PREVIEW = 12;

const exportable = computed(() => props.pairs.filter((e) => e.k).length);

const parsed = computed(() =>
  text.value.trim()
    ? parseVars(text.value, fmt.value === 'auto' ? undefined : (fmt.value as VarFormat))
    : null,
);
const skipped = computed(() =>
  props.skipEmpty && parsed.value ? parsed.value.pairs.filter((e) => !e.v) : [],
);
const usable = computed(() =>
  parsed.value ? (props.skipEmpty ? parsed.value.pairs.filter((e) => e.v) : parsed.value.pairs) : [],
);

const formatOptions = [
  { value: 'auto', label: 'Detect the format', hint: 'read the paste and decide' },
  ...VAR_FORMATS.map((f) => ({ value: f, label: `Read as ${FORMAT_LABEL[f]}` })),
];

async function copy(f: VarFormat): Promise<void> {
  const out = formatVars(props.pairs, f, props.note);
  try {
    await navigator.clipboard.writeText(out);
    fallback.value = '';
    toast('ok', `Copied ${exportable.value} as ${FORMAT_LABEL[f]}.`);
  } catch {
    // Not an error worth a red banner: the text is right here, it just could not be put on the
    // clipboard for you.
    fallback.value = out;
    toast('error', 'Could not reach the clipboard — select the text below and copy it.');
  }
}

function apply(mode: 'replace' | 'merge'): void {
  emit('apply', usable.value, mode);
  text.value = '';
  open.value = false;
}

function selectAll(e: FocusEvent): void {
  (e.target as HTMLTextAreaElement).select();
}
</script>

<template>
  <div class="vario">
    <div class="row">
      <span class="mute vario-lbl">Copy as</span>
      <button
        v-for="f in VAR_FORMATS"
        :key="f"
        type="button"
        class="sm ghost"
        :disabled="!exportable"
        :title="exportable ? `Copy all ${exportable} as ${FORMAT_LABEL[f]}` : 'There is nothing to copy yet'"
        @click="copy(f)"
      >
        {{ FORMAT_LABEL[f] }}
      </button>
      <span class="sep" aria-hidden="true" />
      <button type="button" class="sm" :aria-expanded="open" @click="open = !open">
        {{ open ? 'Close' : 'Paste to fill' }}
      </button>
    </div>

    <div v-if="fallback" class="vario-box">
      <p class="hint">
        The clipboard could not be reached — a browser grants it only on a secure origin, and not
        always then. Select this and copy it by hand.
      </p>
      <textarea
        :value="fallback"
        readonly
        rows="6"
        spellcheck="false"
        aria-label="the exported variables"
        @focus="selectAll"
      />
      <button type="button" class="sm ghost" @click="fallback = ''">Hide</button>
    </div>

    <div v-if="open" class="vario-box">
      <slot />

      <textarea
        v-model="text"
        rows="7"
        spellcheck="false"
        autocomplete="off"
        aria-label="paste variables"
        placeholder="PR=7&#10;REGION=eu-central&#10;&#10;…or two columns of CSV or TSV"
      />

      <div class="row" style="margin-top: var(--s2)">
        <SelectMenu v-model="fmt" label="how to read the paste" :options="formatOptions" />
        <span v-if="parsed" class="mute" style="font-size: var(--t-sm)">
          read as {{ FORMAT_LABEL[parsed.format] }}
        </span>
      </div>

      <template v-if="parsed">
        <p class="hint">
          <b>{{ usable.length }}</b> variable{{ usable.length === 1 ? '' : 's' }} read.
          <template v-if="skipped.length">
            {{ skipped.length }} ({{ skipped.map((e) => e.k).join(', ') }}) had no value and will be
            skipped — a value stored here is never blank.
          </template>
        </p>

        <ul v-if="usable.length" class="kvlist vario-preview">
          <li v-for="e in usable.slice(0, PREVIEW)" :key="e.k">
            <span class="k"><code>{{ e.k }}</code></span>
            <span class="v mono">{{ e.v }}</span>
          </li>
          <li v-if="usable.length > PREVIEW" class="mute">
            and {{ usable.length - PREVIEW }} more
          </li>
        </ul>

        <!--
          Never a silent drop. A line that did not become a variable is shown with its number and
          its text, so the operator can see whether the paste was misread or the source was wrong.
        -->
        <div v-if="parsed.problems.length" class="banner warn">
          <b>
            {{ parsed.problems.length }} line{{ parsed.problems.length === 1 ? '' : 's' }} could not
            be read
          </b>
          <ul class="vario-problems">
            <li v-for="p in parsed.problems" :key="p.line">
              <span class="mute">line {{ p.line }}</span>
              <code>{{ p.text }}</code>
              <span class="dim">{{ p.reason }}</span>
            </li>
          </ul>
          <p>
            They are not imported. Fix them and paste again, or apply what parsed and add the rest
            by hand.
          </p>
        </div>

        <div class="row" style="margin-top: var(--s3)">
          <button
            v-if="replace"
            type="button"
            class="primary"
            :disabled="!usable.length || busy"
            :title="usable.length ? 'Discard the current list and use exactly what was pasted' : 'Nothing parsed to apply'"
            @click="apply('replace')"
          >
            Replace all
          </button>
          <button
            type="button"
            :class="replace ? '' : 'primary'"
            :disabled="!usable.length || busy"
            :title="usable.length ? 'Overwrite matching names, keep the rest, add what is new' : 'Nothing parsed to apply'"
            @click="apply('merge')"
          >
            Merge
          </button>
          <button type="button" class="ghost" @click="open = false">Cancel</button>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.vario {
  margin-top: var(--s3);
}
.vario-lbl {
  font-size: var(--t-xs);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
/* Separates two groups of controls that do opposite things; the gap alone read as one strip. */
.sep {
  width: 1px;
  height: 18px;
  background: var(--line);
  margin-inline: var(--s1);
}
.vario-box {
  margin-top: var(--s3);
  padding: var(--s3);
  border: 1px solid var(--line);
  border-radius: var(--r2);
  max-width: 640px;
}
.vario-box > textarea {
  width: 100%;
}
/* A pasted value can hold newlines; collapsing them here would hide half of what is being applied. */
.vario-preview {
  margin-top: var(--s2);
}
.vario-preview .v {
  white-space: pre-wrap;
}
.vario-problems {
  margin: var(--s2) 0 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: var(--s1);
  font-size: var(--t-sm);
}
/*
 * Two columns, not a wrapping row: a long reason wrapping under `line 5` read as a new paragraph of
 * the banner rather than as that line's explanation.
 */
.vario-problems > li {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 2px var(--s2);
  align-items: baseline;
}
.vario-problems > li > code {
  justify-self: start;
}
.vario-problems > li > .dim {
  grid-column: 2;
}
</style>
