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
 */
defineProps<{
  pending?: boolean;
  disabled?: boolean;
  variant?: 'primary' | 'danger' | 'ghost';
  /** Shown as a native tooltip; use it to say WHY a disabled control is disabled. */
  title?: string;
}>();
</script>

<template>
  <button
    :class="variant"
    :disabled="pending || disabled"
    :title="title"
    :aria-busy="pending ? 'true' : undefined"
  >
    <slot />
    <span v-if="pending" class="progress" aria-hidden="true" />
  </button>
</template>
