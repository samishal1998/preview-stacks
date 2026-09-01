<script setup lang="ts">
/**
 * Axes: the hooks each declares, and whether teardown can be PROVEN.
 *
 * `verifiable: false` means the axis defines no `assert_gone`, so `verify` cannot prove it was
 * cleaned up. That is a THIRD state — neither pass nor fail — and it must never be coloured green:
 * a green verify on an unverifiable axis is exactly the false confidence this tool exists to
 * remove.
 */
import { dep, unverifiableAxes } from '../../composables/useDeployment';
import type { HookName } from '../../api/types';
import InfoHint from '../../components/InfoHint.vue';

/** In the order `up` and `verify` actually use them. */
const HOOKS: HookName[] = ['up', 'assert_live', 'down', 'assert_gone'];
</script>

<template>
  <section class="panel">
    <h2 class="section" style="margin-bottom: var(--s3)">Axes</h2>

    <p v-if="!dep.detail" class="mute">Unavailable until the spec resolves.</p>
    <template v-else>
      <table class="cards">
        <thead>
          <tr>
            <th>Axis</th>
            <th>
              hooks
              <InfoHint label="what the four hooks do">
                <code>up</code> provisions · <code>assert_live</code> checks it exists ·
                <code>down</code> destroys · <code>assert_gone</code> proves it is gone.
              </InfoHint>
            </th>
            <th>Teardown provable?</th>
          </tr>
        </thead>
        <tbody class="stagger">
          <tr v-for="(a, i) in dep.detail.axes" :key="a.name" :style="{ '--i': i }">
            <td class="name" data-label="axis">{{ a.name }}</td>
            <td data-label="hooks">
              <span
                v-for="h in HOOKS"
                :key="h"
                class="badge"
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
            <td colspan="3" class="mute">No axes.</td>
          </tr>
        </tbody>
      </table>

      <!-- The third state, stated once: a green `verify` is silence about these axes, never a pass.
           Colouring them green is the false confidence this tool exists to remove. -->
      <div v-if="unverifiableAxes.length" class="banner warn">
        <b>
          {{ unverifiableAxes.length === 1 ? '1 axis cannot' : `${unverifiableAxes.length} axes cannot` }}
          be verified: {{ unverifiableAxes.join(', ') }}.
        </b>
        <p>A green <code>verify</code> says nothing about them — it is not a pass.</p>
      </div>
    </template>
  </section>
</template>
