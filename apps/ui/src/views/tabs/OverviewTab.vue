<script setup lang="ts">
/** The resolved spec, summarised. Read-only — everything that acts lives on Danger. */
import { computed } from 'vue';
import { dep, isShared, row } from '../../composables/useDeployment';
import { stamp } from '../../composables/useFormat';
import SkeletonList from '../../components/SkeletonList.vue';
import InfoHint from '../../components/InfoHint.vue';

const kindBlurb = computed(() =>
  isShared.value
    ? 'a host singleton previews borrow — no axes, and down is guarded'
    : 'one tenant, created and destroyed constantly, expected to leave nothing behind',
);
</script>

<template>
  <section class="panel">
    <h2 class="section" style="margin-bottom: var(--s3)">Resolved spec</h2>

    <SkeletonList v-if="!dep.detail && !dep.error" :rows="5" />
    <p v-else-if="!dep.detail" class="mute">Unavailable until the spec resolves.</p>

    <template v-else>
      <ul class="kvlist">
        <li>
          <span class="k">
            stack
            <InfoHint label="the difference between id and stack">
              The <b>id</b> is what you named this deployment. The <b>stack</b> is what its spec
              resolved to once the variables were filled in — the compose project name, the hostname
              label, and <code>$STACK</code> inside every hook.
            </InfoHint>
          </span>
          <span class="v mono">{{ dep.detail.stack || '—' }}</span>
        </li>
        <li>
          <span class="k">kind</span>
          <span class="v">
            <span class="badge" :class="dep.detail.kind">{{ dep.detail.kind }}</span>
            <span class="mute" style="margin-left: 8px; font-size: var(--t-sm)">{{ kindBlurb }}</span>
          </span>
        </li>
        <li v-if="row?.specName">
          <span class="k">spec</span>
          <span class="v">
            <b>{{ row.specName }}</b>
            <span class="mute" style="font-size: var(--t-sm)">
              — stored, and shared with any other deployment using it
            </span>
          </span>
        </li>
        <li>
          <span class="k">compose file</span>
          <span class="v mono">
            {{ dep.detail.compose ? dep.detail.compose.file : '— (no compose section)' }}
          </span>
        </li>
        <li v-if="dep.detail.compose">
          <span class="k">profiles</span>
          <span class="v">
            <span v-for="p in dep.detail.compose.profiles" :key="p" class="badge" style="margin-right: 4px">
              {{ p }}
            </span>
            <span v-if="!dep.detail.compose.profiles.length" class="mute">
              none — a bare up starts every service
            </span>
          </span>
        </li>
        <li v-if="dep.detail.compose">
          <span class="k">overlays</span>
          <span class="v">
            <span v-for="o in dep.detail.compose.overlays" :key="o" class="mono" style="margin-right: 8px">{{ o }}</span>
            <span v-if="!dep.detail.compose.overlays.length" class="mute">none</span>
          </span>
        </li>
        <li>
          <span class="k">axes</span>
          <span class="v">
            {{ dep.detail.axes.length }}
            <RouterLink v-if="dep.detail.axes.length" :to="`/deployments/${encodeURIComponent(dep.id)}/axes`">
              view →
            </RouterLink>
          </span>
        </li>
        <li>
          <span class="k">requires</span>
          <span class="v">{{ dep.detail.requires.length }}</span>
        </li>
        <li><span class="k">created</span><span class="v">{{ stamp(dep.detail.createdAt) }}</span></li>
        <li><span class="k">updated</span><span class="v">{{ stamp(dep.detail.updatedAt) }}</span></li>
      </ul>
    </template>
  </section>
</template>
