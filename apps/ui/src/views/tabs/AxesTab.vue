<script setup lang="ts">
/**
 * Axes: the hooks each declares, and whether teardown can be PROVEN.
 *
 * `verifiable: false` means the axis defines no `assert_gone`, so `verify` cannot prove it was
 * cleaned up. That is a THIRD state — neither pass nor fail — and it must never be coloured green:
 * a green verify on an unverifiable axis is exactly the false confidence this tool exists to
 * remove.
 */
import { dep, isShared, unverifiableAxes } from '../../composables/useDeployment';
import type { HookName } from '../../api/types';

/** In the order `up` and `verify` actually use them. */
const HOOKS: HookName[] = ['up', 'assert_live', 'down', 'assert_gone'];
</script>

<template>
  <section class="panel">
    <h2 class="section" style="margin-bottom: var(--s3)">
      Axes <span class="mute">(provisioned in order, destroyed in reverse)</span>
    </h2>

    <p v-if="!dep.detail" class="mute">Unavailable until the spec resolves.</p>
    <template v-else>
      <p class="hint" style="margin: 0 0 var(--s3)">
        <code>up</code> provision · <code>assert_live</code> it exists · <code>down</code> destroy ·
        <code>assert_gone</code> proven gone. Hook <em>bodies</em> are never sent to this page — a
        hook is a shell string that routinely carries a token inline.
      </p>

      <table class="cards">
        <thead>
          <tr>
            <th>axis</th>
            <th>hooks</th>
            <th>teardown provable?</th>
          </tr>
        </thead>
        <tbody class="stagger">
          <tr v-for="(a, i) in dep.detail.axes" :key="a.name" :style="{ '--i': i }">
            <td class="name" data-label="axis">{{ a.name }}</td>
            <td data-label="hooks">
              <span
                v-for="h in HOOKS"
                :key="h"
                class="badge mono"
                :class="{ off: !a.hooks.includes(h) }"
                style="margin-right: 4px"
                >{{ h }}</span
              >
            </td>
            <td data-label="provable">
              <span v-if="a.verifiable" class="badge ok">assert_gone</span>
              <span
                v-else
                class="badge warn"
                title="verify cannot prove this axis was cleaned up — this is not a pass"
                >unverifiable</span
              >
            </td>
          </tr>
          <tr v-if="!dep.detail.axes.length">
            <td colspan="3" class="mute">
              No axes<span v-if="isShared">
                — a shared singleton has nothing to isolate and nothing to prove gone</span
              >.
            </td>
          </tr>
        </tbody>
      </table>

      <div v-if="unverifiableAxes.length" class="banner warn">
        <b>{{ unverifiableAxes.length }} axis/axes cannot be verified:</b>
        <span class="mono">{{ unverifiableAxes.join(', ') }}</span
        >.
        <p>
          They define no <code>assert_gone</code>, so a green <code>verify</code> is
          <em>silence</em> about them, not proof they are gone. Add an <code>assert_gone</code> that
          <b>fails closed</b> — probe that the check itself can work, then assert absence:
        </p>
        <pre
          class="raw"
          style="color: var(--fg-dim)"
        >&lt;probe-is-usable&gt; || exit 1
! &lt;probe-for-this-resource&gt;</pre>
        <p>
          A bare <code>! &lt;probe&gt;</code> exits 0 whenever the probe itself fails — a missing
          CLI, an expired token — which turns “I could not tell” into “it is gone”.
        </p>
      </div>
    </template>
  </section>
</template>
