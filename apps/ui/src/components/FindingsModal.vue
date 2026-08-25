<script setup lang="ts">
/**
 * The runtime findings, as one button that opens them rather than a stack of banners.
 *
 * WHAT WAS WRONG. Every finding rendered as its own inline banner, unbounded. A deployment with a
 * dozen of them — which is exactly the deployment worth looking at — pushed the routing table and the
 * container table off the bottom of the screen, so the page that exists to show you what is running
 * showed you paragraphs about it instead.
 *
 * WHAT THE BANNERS DID WELL, AND IS KEPT. A `warn` announced itself: you could not open the tab and
 * miss it. So the trigger carries the count and the highest severity as COLOUR, not just a label —
 * three warnings and none look different from across the room, which a plain "Findings" button would
 * not. An empty list renders nothing at all: there is no button to wonder about.
 *
 * Built on `HelpModal`'s idiom rather than a second one — the same Reka Dialog, the same
 * `.help-*` classes, so focus trap, focus restore, scroll lock and Escape behave identically. It is a
 * separate component only because the trigger is the whole point here and `HelpModal`'s is a fixed
 * `?` glyph.
 */
import { computed } from 'vue';
import { DialogClose, DialogContent, DialogOverlay, DialogPortal, DialogRoot, DialogTitle, DialogTrigger } from 'reka-ui';
import type { RuntimeResponse } from '../api/types';

const props = defineProps<{ findings: RuntimeResponse['findings'] }>();

/** Blocking first, then worth checking, then notes — the order the banners rendered in. */
const ORDER = { error: 0, warn: 1, info: 2 } as const;
const sorted = computed(() => [...props.findings].sort((a, b) => ORDER[a.level] - ORDER[b.level]));
/** The highest severity present. Falls out of the sort — there is no second ranking to disagree. */
const worst = computed(() => sorted.value[0]?.level ?? 'info');

/** The banners' own words, so the button and the thing it opens say the same thing. */
const LABEL = { error: 'Blocking', warn: 'Worth checking', info: 'Notes' } as const;
const BADGE = { error: 'failed', warn: 'warn', info: 'info' } as const;
/** `info` has no banner tint — a note is not a state. */
const BANNER = { error: 'failed', warn: 'warn', info: 'plain' } as const;
</script>

<template>
  <DialogRoot v-if="findings.length">
    <DialogTrigger class="btn sm findings-btn" :class="worst">
      <span class="badge" :class="BADGE[worst]">{{ findings.length }}</span>
      {{ LABEL[worst] }}
    </DialogTrigger>

    <DialogPortal>
      <DialogOverlay class="scrim help-scrim" />
      <DialogContent class="help-modal">
        <div class="help-head">
          <DialogTitle class="help-title">What pstack noticed</DialogTitle>
          <DialogClose class="ghost sm" aria-label="Close">Close</DialogClose>
        </div>
        <div class="help-body">
          <div v-for="(f, i) in sorted" :key="i" class="banner" :class="BANNER[f.level]">
            <b>{{ LABEL[f.level] }}</b>
            <p>{{ f.message }}</p>
          </div>
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

<style scoped>
/*
 * The trigger sits where the banners did, so it inherits their vertical rhythm (`.banner` is
 * `margin: var(--s3) 0`) rather than butting against the panel below it.
 */
.findings-btn {
  margin: var(--s3) 0;
}
/*
 * `error` borrows `button.danger`'s treatment by name; `warn` has no button variant in the system, so
 * this is the same recipe in `--leak`. Both are the badge's colour repeated at the button's edge —
 * one hue per state, not two.
 */
.findings-btn.error {
  background: var(--fail-soft);
  border-color: color-mix(in srgb, var(--fail) 45%, transparent);
  color: var(--fail);
}
.findings-btn.error:hover:not(:disabled) {
  background: color-mix(in srgb, var(--fail) 26%, transparent);
  border-color: color-mix(in srgb, var(--fail) 62%, transparent);
}
.findings-btn.warn {
  background: var(--leak-soft);
  border-color: color-mix(in srgb, var(--leak) 45%, transparent);
  color: var(--leak);
}
.findings-btn.warn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--leak) 26%, transparent);
  border-color: color-mix(in srgb, var(--leak) 62%, transparent);
}
/*
 * Inside the modal the banners ARE the list, so the body's own padding already spaces the first and
 * last from the edges. `.help-body > * + *` keeps the gap between them.
 */
.help-body > .banner:first-child {
  margin-top: 0;
}
.help-body > .banner:last-child {
  margin-bottom: 0;
}
</style>
