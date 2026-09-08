<script setup lang="ts">
/**
 * The `?` sheet.
 *
 * Reka owns modality, and that is the whole point of it not being a hand-rolled scrim any more:
 * the previous version declared `aria-modal="true"` while doing none of what that promises —
 * opening it left focus on `<body>`, and three Tabs put you on a nav link BEHIND the sheet. Reka
 * brings the focus trap, focus restore on close, the scroll lock and Escape, and it is already
 * this app's idiom (`HelpModal`, `FindingsModal`).
 */
import {
  DialogRoot, DialogPortal, DialogOverlay, DialogContent, DialogTitle, DialogClose,
} from 'reka-ui';
import { SHORTCUTS } from '../composables/useShortcuts';

defineProps<{ open: boolean }>();
const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <DialogRoot :open="open" @update:open="(v: boolean) => !v && emit('close')">
    <DialogPortal>
      <DialogOverlay class="scrim help-scrim" />
      <DialogContent class="sheet" aria-label="Keyboard shortcuts">
        <div class="row">
          <DialogTitle style="font-size: var(--t-lg); font-weight: 650">Keyboard shortcuts</DialogTitle>
          <span class="grow" />
          <DialogClose class="ghost sm">Close</DialogClose>
        </div>
        <dl>
          <template v-for="s in SHORTCUTS" :key="s.keys">
            <dt>
              <kbd v-for="k in s.keys.split(' ')" :key="k" style="margin-left: 4px">{{ k }}</kbd>
            </dt>
            <dd>{{ s.what }}</dd>
          </template>
        </dl>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
