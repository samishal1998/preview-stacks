<script setup lang="ts">
/** The `?` sheet. Closes on Escape (handled globally), on the scrim, and on its own button. */
import { SHORTCUTS } from '../composables/useShortcuts';

defineProps<{ open: boolean }>();
const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <Transition name="fade">
    <div
      v-if="open"
      class="scrim"
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
      @click.self="emit('close')"
    >
      <div class="sheet">
        <div class="row">
          <h2 style="font-size: var(--t-lg); font-weight: 650">Keyboard shortcuts</h2>
          <span class="grow" />
          <button class="ghost sm" @click="emit('close')">Close</button>
        </div>
        <dl>
          <template v-for="s in SHORTCUTS" :key="s.keys">
            <dt>
              <kbd v-for="k in s.keys.split(' ')" :key="k" style="margin-left: 4px">{{ k }}</kbd>
            </dt>
            <dd>{{ s.what }}</dd>
          </template>
        </dl>
      </div>
    </div>
  </Transition>
</template>
