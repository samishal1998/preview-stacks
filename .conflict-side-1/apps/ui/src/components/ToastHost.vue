<script setup lang="ts">
/**
 * Stacked, dismissible toasts. `aria-live="polite"` so a job finishing is announced without
 * stealing focus from whatever the operator is typing.
 */
import { toasts, dismissToast } from '../composables/useToasts';
</script>

<template>
  <div class="toasts" aria-live="polite" aria-atomic="false">
    <TransitionGroup name="toast">
      <div v-for="t in toasts" :key="t.id" class="toast" :class="t.kind">
        <div>
          <div>{{ t.text }}</div>
          <RouterLink v-if="t.to" :to="t.to">{{ t.toLabel ?? 'Open' }} →</RouterLink>
        </div>
        <button class="x" :aria-label="`dismiss: ${t.text}`" @click="dismissToast(t.id)">✕</button>
      </div>
    </TransitionGroup>
  </div>
</template>
