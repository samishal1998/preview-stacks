<script setup lang="ts">
/**
 * A button that disables itself the moment it is pressed and shows inline progress until the
 * request settles.
 *
 * Optimistic only about the DISABLED state, never about the outcome: every action here starts a
 * job that destroys or creates infrastructure, so the button says "in flight", not "done". The
 * result is reported by the caller once the server has actually answered.
 *
 * The progress bar sits inside the button rather than replacing the label, so the button keeps its
 * width and the page does not reflow under the pointer mid-click.
 *
 * ── `confirm` ────────────────────────────────────────────────────────────────────────────────────
 *
 * Pass `confirm="…"` and the first click ARMS the button instead of acting: the label becomes the
 * confirmation question, and only a second click within a few seconds emits `run`. Anything else —
 * moving away, pressing Escape, waiting — disarms it.
 *
 * Two clicks in place rather than a modal, because a dialog for "delete this row" blocks the page
 * for a decision that is already visible; and rather than `window.confirm`, which is a native OS
 * panel this app deliberately avoids everywhere else.
 *
 * THIS PROP USED TO NOT EXIST. Four call sites (Delete on each Variables/Secrets row, Delete and
 * Revoke in Users) already passed `:confirm` and `@run` — so `confirm` fell through as a stray DOM
 * attribute and `run` was emitted by nothing. Those buttons rendered, took the click, and did
 * nothing at all. Implementing the contract they were written against is the fix.
 */
import { onBeforeUnmount, ref } from 'vue';

const props = defineProps<{
  pending?: boolean;
  disabled?: boolean;
  variant?: 'primary' | 'danger' | 'ghost';
  /** Shown as a native tooltip; use it to say WHY a disabled control is disabled. */
  title?: string;
  /** When set, the click is two-stage and this is the question asked in between. */
  confirm?: string;
}>();

const emit = defineEmits<{ (e: 'run'): void }>();

const armed = ref(false);
let timer: ReturnType<typeof setTimeout> | null = null;

function disarm(): void {
  armed.value = false;
  if (timer) clearTimeout(timer);
  timer = null;
}

function onClick(): void {
  if (!props.confirm) return; // plain button: the parent's own @click handles it
  if (armed.value) {
    disarm();
    emit('run');
    return;
  }
  armed.value = true;
  // Auto-disarm: an armed destructive button left on screen is a trap for the next click, and the
  // next click is often someone scrolling back to a row they had already decided against.
  timer = setTimeout(disarm, 5_000);
}

onBeforeUnmount(disarm);
</script>

<template>
  <button
    :class="[variant, armed && 'armed']"
    :disabled="pending || disabled"
    :title="title"
    :aria-busy="pending ? 'true' : undefined"
    @click="onClick"
    @blur="disarm"
    @keydown.esc="disarm"
  >
    <span v-if="armed">{{ confirm }}</span>
    <slot v-else />
    <span v-if="pending" class="progress" aria-hidden="true" />
  </button>
</template>
