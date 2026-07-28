<script setup lang="ts">
/**
 * A 409, explained.
 *
 * "409" on this API always means the same thing about consequences — **refused, and nothing was
 * started** — but four different things about the cause, and guessing wrong sends the operator
 * down the wrong path. The classification is done on payload SHAPE in `client.ts`, never on the
 * message text, and the server's own message is always printed verbatim underneath.
 *
 * Note what is deliberately NOT distinguished: "a job is in flight" and "docker did not answer"
 * are shape-identical, so this renders neither claim — it prints the message and offers a link to
 * a running job only when one demonstrably exists.
 */
import type { Conflict } from '../api/client';

defineProps<{ conflict: Conflict; jobId?: string }>();
</script>

<template>
  <div class="banner" :class="conflict.kind === 'shared' ? 'leaked' : 'info'" role="alert">
    <b>409 — refused, and nothing was started.</b>
    <pre class="raw" style="color: var(--fg-dim); max-height: 200px">{{ conflict.text }}</pre>

    <p v-if="conflict.kind === 'shared'">
      Type the stack name in the confirmation field above and press <code>down</code> again.
    </p>
    <p v-else-if="conflict.kind === 'containers'">
      Run <code>down</code> first. Forgetting the record now would orphan those containers beyond
      the control plane's view: nothing left would know their stack name, their axes, or how to
      tear them down.
    </p>
    <p v-else-if="conflict.kind === 'referenced'">
      These deployments still point at this spec:
      <span class="mono">{{ conflict.deployments.join(', ') }}</span
      >. Deleting it would leave them unresolvable — and a deployment that cannot be resolved can
      never be torn down.
    </p>
    <p v-else-if="jobId">
      <RouterLink :to="`/jobs/${encodeURIComponent(jobId)}`">Follow the running job →</RouterLink>
    </p>
    <p v-else>Nothing was retried. Wait for the lock to clear, then try again.</p>
  </div>
</template>
