<script setup lang="ts">
/**
 * A job's step results.
 *
 * The legend is part of the component because the four states only mean anything together:
 * `unverifiable` is not a pass, and `leaked` is not the same problem as `failed`.
 */
import type { StepResult } from '../api/types';
import { stepMark, stepState, stepText } from '../composables/useSteps';

defineProps<{ steps: StepResult[] }>();
</script>

<template>
  <div>
    <ul class="steps stagger">
      <li
        v-for="(s, i) in steps"
        :key="`${s.phase}-${s.axis}-${i}`"
        class="step"
        :style="{ '--i': i }"
      >
        <span class="mark" :class="`s-${stepState(s)}`" aria-hidden="true">{{ stepMark(s) }}</span>
        <span>
          <span class="who">{{ s.axis }}</span>
          <span class="dim" style="font-size: var(--t-sm)"> · {{ s.phase }}</span>
          <span class="badge" :class="stepState(s)" style="margin-left: var(--s2)">
            {{ stepState(s) }}
          </span>
          <div class="msg" :class="`s-${stepState(s)}`">{{ stepText(s) }}</div>
        </span>
      </li>
    </ul>

    <p class="hint">
      <span class="s-ok">✓ ok</span> ·
      <span class="s-failed">✗ failed</span> ·
      <span class="s-leaked">! leaked — survived teardown</span> ·
      <span class="s-unverifiable">? unverifiable — nothing to check, not a pass</span>.
      A <code>down</code> step marked ok may still carry a <code>non-fatal:</code> note: teardown is
      best-effort by design, so aborting halfway would leave more behind than continuing.
    </p>
  </div>
</template>
