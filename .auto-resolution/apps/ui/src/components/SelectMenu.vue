<script setup lang="ts">
/**
 * A dropdown that is ours, on primitives that are not.
 *
 * WHY NOT `<select>`. Its *popup* is drawn by the operating system, not the page: `appearance: none`
 * restyles the closed control and does nothing to the open list. A native select therefore looks
 * like macOS on macOS and Windows on Windows, in the middle of a UI that looks like neither — and it
 * cannot carry a hint, a badge, or a tick. Everything a stylesheet can reach is the half that does
 * not matter.
 *
 * WHY REKA-UI RATHER THAN THE HAND-ROLLED LISTBOX THIS REPLACES. The first version here was ~200
 * lines of my own roving-focus, type-ahead, flip-when-there-is-no-room-below and click-outside
 * handling. All of that is real behaviour with real edge cases — pointer vs keyboard focus, touch,
 * scroll containment, RTL, a trigger inside an `overflow: hidden` ancestor — and none of it is this
 * product's problem. Reka is the headless primitive set shadcn-vue is itself built on (~1.5M
 * downloads/week, `vue` its only peer), so it brings the behaviour and none of the styling
 * opinions: every rule below is still ours, on our tokens.
 *
 * NOT shadcn-vue: it generates Tailwind-styled components, and this app has no Tailwind — adopting
 * it would mean adding a whole styling pipeline to get components we then restyle anyway.
 */
import {
  SelectContent,
  SelectItem,
  SelectItemIndicator,
  SelectItemText,
  SelectPortal,
  SelectRoot,
  SelectTrigger,
  SelectViewport,
} from 'reka-ui';

export type SelectOption = { value: string; label: string; hint?: string };

defineProps<{
  options: SelectOption[];
  /** Accessible name. Rendered as an aria-label, never as visible chrome. */
  label: string;
  disabled?: boolean;
  /** Shown when the value matches no option — e.g. a filter that has not been chosen yet. */
  placeholder?: string;
  /**
   * Forwarded to the trigger so a visible `<label for>` still targets it. Without this the label
   * renders but clicking it focuses nothing — the association a native `<select>` gave for free.
   */
  id?: string;
}>();

/** `defineModel` rather than a prop/emit pair — Reka's root is already v-model shaped. */
const value = defineModel<string>({ required: true });
</script>

<template>
  <SelectRoot v-model="value" :disabled="disabled">
    <SelectTrigger :id="id" class="selm-trigger" :aria-label="label">
      <!--
        The label is resolved here rather than with `<SelectValue>`. Reka's SelectValue reads the
        text of the selected `SelectItemText`, and those live in a portal that is UNMOUNTED while
        closed — so a freshly-loaded page renders an empty trigger until the list has been opened
        once. Deriving it from `options` is one line and cannot be empty.
      -->
      <span class="selm-value">{{ options.find((o) => o.value === value)?.label ?? placeholder ?? '—' }}</span>
      <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" class="selm-caret">
        <path
          d="m6 9 6 6 6-6"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
    </SelectTrigger>

    <!-- Portalled so a trigger inside a panel with `overflow: hidden` cannot clip its own list. -->
    <SelectPortal>
      <SelectContent class="selm-list" position="popper" :side-offset="6">
        <SelectViewport>
          <SelectItem v-for="o in options" :key="o.value" :value="o.value" class="selm-opt">
            <SelectItemText>{{ o.label }}</SelectItemText>
            <span v-if="o.hint" class="selm-hint">{{ o.hint }}</span>
            <SelectItemIndicator class="selm-tick">
              <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                <path
                  d="m5 13 4 4L19 7"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2.5"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
              </svg>
            </SelectItemIndicator>
          </SelectItem>
        </SelectViewport>
      </SelectContent>
    </SelectPortal>
  </SelectRoot>
</template>
