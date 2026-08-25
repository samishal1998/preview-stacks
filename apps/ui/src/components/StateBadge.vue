<script setup lang="ts">
/**
 * The job states, and only these. Kept in one component so the colour of `leaked` can never drift
 * from the colour of `failed` in one view and not another:
 *
 *   ok  green · failed  red · leaked  AMBER · running  blue, pulsing
 *   queued  grey · cancelled  grey · superseded  grey
 *
 * THE THREE GREYS ARE DELIBERATE, and they are grey because none of them is an alarm:
 *
 *   `cancelled` — someone meant to stop it. Not `ok` either: whatever the job had already done was
 *   left exactly as it was, which is why the job page puts a banner under this badge.
 *   `queued`    — it has not started. Waiting is the system working as designed (one job per stack,
 *   and a host-wide cap), so it must not borrow `running`'s blue, and it must not PULSE — the pulse
 *   is what says "something is happening right now", and nothing is.
 *   `superseded` — a newer job for the same stack replaced it while it waited. It never ran, so
 *   there is nothing out there from it; routine, and quieter than cancelled if anything.
 *
 * Only `running` pulses (`.badge.running .dot` in app.css). The three greys fall through to the
 * base `.badge` rule with no colour of their own — which is the correct severity, not an oversight,
 * so do not "finish" them with a hue.
 *
 * `leaked` must be unmistakably distinct from `failed`. They are different problems with different
 * owners — "teardown errored" versus "teardown ran and something survived" — which is why the CLI
 * gives leaked its own exit code, and why this app never renders them in the same colour.
 */
import type { JobState } from '../api/types';
import { sentence } from '../composables/useFormat';

defineProps<{ state: JobState }>();
</script>

<template>
  <span class="badge" :class="state">
    <span class="dot" />
    {{ sentence(state) }}
  </span>
</template>
