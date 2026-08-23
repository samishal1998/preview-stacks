<script setup lang="ts">
/**
 * A `?` that opens a modal.
 *
 * WHY THIS AND NOT `InfoHint`. `InfoHint` is a small `[i]` popover for a sentence or two — the right
 * shape for "what does this field mean". It is the wrong shape for the several paragraphs some of
 * these surfaces genuinely need: a 300-character bubble anchored to an icon either overflows its
 * container or wraps into a column two words wide.
 *
 * Those paragraphs used to sit permanently on the page as banners. That is the actual problem: an
 * explanation everyone reads once and nobody reads again, charged to every visit forever. A `?`
 * costs one glyph in the header and gives the reader the whole thing when they want it.
 *
 * WHAT STAYS ON THE PAGE. State, not explanation. "Docker did not answer" is a banner because it is
 * true right now and changes what you can do; "here is how Traefik resolves a container IP" is help,
 * and belongs behind the `?`.
 *
 * Built on Reka's Dialog: focus trap, focus restore, scroll lock, Escape, and `aria-modal` wiring
 * are all behaviour with edge cases, and none of them are this product's problem.
 */
import { DialogClose, DialogContent, DialogOverlay, DialogPortal, DialogRoot, DialogTitle, DialogTrigger } from 'reka-ui';

defineProps<{
  /** The question the modal answers, e.g. "How routing works". Becomes the dialog's title. */
  title: string;
  /** Accessible name for the trigger — screen readers hear this instead of "question mark". */
  label?: string;
}>();
</script>

<template>
  <DialogRoot>
    <DialogTrigger class="helpq" :aria-label="label ?? title">
      <svg viewBox="0 0 24 24" width="13" height="13" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round">
        <path d="M9.2 9a2.9 2.9 0 1 1 3.8 2.8c-.7.3-1 .9-1 1.6v.4" />
        <circle cx="12" cy="17.6" r="0.9" fill="currentColor" stroke="none" />
      </svg>
    </DialogTrigger>

    <DialogPortal>
      <DialogOverlay class="scrim help-scrim" />
      <DialogContent class="help-modal">
        <div class="help-head">
          <DialogTitle class="help-title">{{ title }}</DialogTitle>
          <DialogClose class="ghost sm" aria-label="Close">Close</DialogClose>
        </div>
        <div class="help-body">
          <slot />
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
