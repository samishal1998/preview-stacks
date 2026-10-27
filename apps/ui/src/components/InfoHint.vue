<script setup lang="ts">
/**
 * An `i` affordance that reveals detail on demand.
 *
 * WHY. Most of what this UI has to say is context, not instruction: why a control is disabled, what
 * a status word means, where the data directory is. Printed permanently it competes with the thing
 * the operator came for; deleted it leaves a dead end. So it lives behind a marker that costs one
 * character of layout.
 *
 * Hover AND click, deliberately: hover alone is unreachable on touch and by keyboard, so the button
 * is a real `<button>` that toggles a pinned state, while pointer-over and focus show it
 * transiently. Pinned survives the pointer leaving — you can read a long hint without hovering a
 * moving target. Escape closes, and so does a click anywhere else.
 *
 * Not the `title` attribute: it waits a second, cannot be styled, cannot hold a link, and never
 * appears on touch at all.
 *
 * The trigger is lucide's `Info` glyph rather than a typed letter `i`. A text glyph inherits font
 * metrics and italics, so it never sits centred in its own circle and reads as a stray character.
 */
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { Info } from 'lucide-vue-next';

const props = defineProps<{
  /** Accessible name for the trigger — say what the hint is about, e.g. "about leaked axes". */
  label?: string;
  /** Anchor the bubble to the trigger's end edge; use in a right-aligned row so it stays on screen. */
  align?: 'start' | 'end';
  /** Open upward. Required for a hint near the bottom of the viewport, e.g. in the rail footer. */
  side?: 'top' | 'bottom';
}>();

const pinned = ref(false);
const hovered = ref(false);
const root = ref<HTMLElement | null>(null);
const bub = ref<HTMLElement | null>(null);

/*
 * The bubble is anchored to its trigger, and a trigger far enough along a phone-width line pushes
 * it off the screen (and the page sideways). Shift it back inside an 8px margin once it renders.
 * `clientWidth`, not `innerWidth`: the latter counts a classic scrollbar.
 */
watch(
  () => pinned.value || hovered.value,
  async (open) => {
    if (!open) return;
    await nextTick();
    const el = bub.value;
    if (!el) return;
    el.style.translate = '';
    const r = el.getBoundingClientRect();
    const vw = document.documentElement.clientWidth;
    const dx = r.right > vw - 8 ? vw - 8 - r.right : r.left < 8 ? 8 - r.left : 0;
    if (dx) el.style.translate = `${dx}px 0`;
  },
);

/** Any click that is not inside this hint unpins it — the usual dismissal for a transient bubble. */
function onDocPointerDown(e: PointerEvent): void {
  if (pinned.value && root.value && !root.value.contains(e.target as Node)) pinned.value = false;
}
function onKey(e: KeyboardEvent): void {
  if (e.key === 'Escape' && pinned.value) pinned.value = false;
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocPointerDown);
  document.addEventListener('keydown', onKey);
});
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocPointerDown);
  document.removeEventListener('keydown', onKey);
});
</script>

<template>
  <span ref="root" class="hint-wrap">
    <button
      type="button"
      class="hint-btn"
      :class="{ on: pinned }"
      :aria-label="props.label ?? 'More information'"
      :aria-expanded="pinned || hovered"
      @click="pinned = !pinned"
      @pointerenter="hovered = true"
      @pointerleave="hovered = false"
      @focus="hovered = true"
      @blur="hovered = false"
    >
      <Info :size="15" :stroke-width="2" aria-hidden="true" />
    </button>
    <Transition name="hint">
      <span
        v-if="pinned || hovered"
        ref="bub"
        class="hint-bub"
        :class="[props.align === 'end' ? 'to-end' : 'to-start', props.side === 'top' ? 'above' : 'below']"
      >
        <slot />
      </span>
    </Transition>
  </span>
</template>
