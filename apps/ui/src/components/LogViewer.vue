<script setup lang="ts">
/**
 * The log surface — used by the job page (structured events) and the container-logs tab (raw text
 * that the parent parses into rows).
 *
 * WHY NOT A LIBRARY. There is no widely-adopted Vue log viewer to reach for (the React ones —
 * react-lazylog and friends — have no Vue counterpart with real adoption), and xterm — already a
 * dependency for the terminal — renders to canvas, so log text would stop being selectable,
 * copyable and findable with ⌘F. For LOGS those are the whole point. What a log view actually
 * needs is small: a gutter that never wraps, a message that wraps with a hanging indent, stickiness
 * at the bottom, and cheap rendering for thousands of rows (`content-visibility: auto` — the
 * browser skips layout for off-screen rows, no virtualisation library required).
 *
 * THE GUTTER IS TIME-OF-DAY, NOT A FULL TIMESTAMP. `2026-08-04 03:47:45` per line is 19 characters
 * of the same date repeated hundreds of times — on a narrow panel it wrapped every entry onto five
 * or six lines. Within one log the date almost never changes, so each row carries `03:47:45` and a
 * date SEPARATOR row appears only when the day actually flips. The full stamp survives in the
 * row's tooltip.
 *
 * FOLLOW IS A STATE THE USER CONTROLS BY SCROLLING. Pinned to the bottom, new lines keep it there;
 * one scroll up releases it (reading must never be yanked away); the "Follow" chip puts it back.
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { toast } from '../composables/useToasts';
import type { LogRow } from './log-viewer-types';

const props = defineProps<{
  rows: LogRow[];
  /** Renders the live pulse and keeps follow meaningful; a finished log needs neither. */
  live?: boolean;
  /** What Copy puts on the clipboard — the parent knows the faithful raw form, this view does not. */
  copyText?: string;
  emptyText?: string;
}>();

const box = ref<HTMLElement | null>(null);
const follow = ref(true);

function atBottom(): boolean {
  const el = box.value;
  return !!el && el.scrollHeight - el.scrollTop - el.clientHeight < 24;
}

function onScroll(): void {
  // The USER moved — follow reflects where they left the viewport, with no flag juggling for
  // programmatic scrolls: after scrollTo(bottom), atBottom() is simply true.
  follow.value = atBottom();
}

async function stick(): Promise<void> {
  await nextTick();
  const el = box.value;
  if (el && follow.value) el.scrollTop = el.scrollHeight;
}

watch(() => props.rows.length, stick);
onMounted(stick);

function resume(): void {
  follow.value = true;
  void stick();
}

async function copy(): Promise<void> {
  const text = props.copyText ?? props.rows.filter((r) => !r.separator).map((r) => (r.gutter ? `${r.gutter}  ${r.text}` : r.text)).join('\n');
  try {
    await navigator.clipboard.writeText(text);
    toast('ok', 'Copied.');
  } catch {
    toast('error', 'Could not copy — your browser blocked clipboard access.');
  }
}

const count = computed(() => props.rows.filter((r) => !r.separator).length);

// A page-level listener would steal browser find; scoping ⌘F to the box when it has focus would be
// surprising. The box is plain DOM precisely so the browser's own find works — nothing to do here.
onBeforeUnmount(() => {});
</script>

<template>
  <div class="lv">
    <div class="lv-bar">
      <span class="lv-count">{{ count }} line{{ count === 1 ? '' : 's' }}</span>
      <span v-if="live" class="badge running"><span class="dot pulse" />live</span>
      <span class="grow" />
      <button v-if="live && !follow" class="ghost sm" @click="resume">↓ Follow</button>
      <button class="ghost sm" :disabled="!count" @click="copy">Copy</button>
      <slot name="actions" />
    </div>

    <div ref="box" class="lv-box" role="log" aria-live="polite" @scroll.passive="onScroll">
      <p v-if="!rows.length" class="lv-empty">{{ emptyText ?? 'No output.' }}</p>
      <template v-for="r in rows" :key="r.key">
        <div v-if="r.separator" class="lv-sep"><span>{{ r.text }}</span></div>
        <div v-else class="lv-row" :class="r.tone">
          <span v-if="r.gutter !== undefined" class="lv-gutter" :title="r.gutterTitle">{{ r.gutter }}</span>
          <span class="lv-msg">{{ r.text }}</span>
        </div>
      </template>
    </div>
  </div>
</template>
