<script setup lang="ts">
/** The resolved spec, summarised. Read-only — everything that acts lives on Danger. */
import { dep, row } from '../../composables/useDeployment';
import { sentence, stamp } from '../../composables/useFormat';
import SkeletonList from '../../components/SkeletonList.vue';
</script>

<template>
  <section class="panel">
    <div class="phead">
      <h2 class="section">Resolved spec</h2>
      <span class="grow" />
      <RouterLink v-if="dep.detail" :to="`/submit?from=${encodeURIComponent(dep.id)}`">
        Duplicate
      </RouterLink>
      <span v-if="dep.detail" class="mute">·</span>
      <RouterLink v-if="dep.detail" :to="`/submit/${encodeURIComponent(dep.id)}`">
        Edit spec &amp; compose →
      </RouterLink>
    </div>

    <SkeletonList v-if="!dep.detail && !dep.error" :rows="5" />
    <p v-else-if="!dep.detail" class="mute">Spec not resolved.</p>

    <template v-else>
      <ul class="kvlist">
        <li>
          <span class="k">Stack</span>
          <span class="v mono">{{ dep.detail.stack || '—' }}</span>
        </li>
        <li>
          <span class="k">Kind</span>
          <span class="v">
            <span class="badge" :class="dep.detail.kind">{{ sentence(dep.detail.kind) }}</span>
          </span>
        </li>
        <li v-if="row?.specName">
          <span class="k">Spec</span>
          <span class="v"><b>{{ row.specName }}</b></span>
        </li>
        <li>
          <span class="k">Compose file</span>
          <span class="v mono">
            {{ dep.detail.compose ? dep.detail.compose.file : 'none' }}
          </span>
        </li>
        <li v-if="dep.detail.compose">
          <span class="k">Profiles</span>
          <span class="v">
            <span v-for="p in dep.detail.compose.profiles" :key="p" class="badge" style="margin-right: 4px">
              {{ p }}
            </span>
            <span v-if="!dep.detail.compose.profiles.length" class="mute">none</span>
          </span>
        </li>
        <li v-if="dep.detail.compose?.subdomains?.length">
          <span class="k">Subdomains</span>
          <span class="v">
            <div v-for="s in dep.detail.compose.subdomains" :key="s.profile">
              <span class="mono">*.{{ s.host }}</span>
              <span class="mute" style="font-size: var(--t-sm)">
                → {{ s.profile }}<template v-if="s.depth === 'any'"> · any depth, HTTP only</template>
              </span>
            </div>
          </span>
        </li>
        <li v-if="dep.detail.compose">
          <span class="k">Orchestrator</span>
          <span class="v">{{ dep.detail.orchestrator ?? 'compose' }}</span>
        </li>
        <li v-if="dep.detail.sleep">
          <span class="k">Sleep policy</span>
          <span class="v">
            <span v-if="dep.detail.sleep.idle"><b>{{ dep.detail.sleep.idle }}</b> idle</span>
            <span v-if="dep.detail.sleep.idle && dep.detail.sleep.after" class="mute"> · </span>
            <span v-if="dep.detail.sleep.after"><b>{{ dep.detail.sleep.after }}</b> after deploy</span>
          </span>
        </li>
        <li v-if="dep.detail.asleep">
          <span class="k">Asleep</span>
          <span class="v">
            since {{ stamp(dep.detail.asleep.since) }}
            <span class="mute">({{ dep.detail.asleep.reason }})</span>
            <div v-if="dep.detail.asleep.hosts.length || dep.detail.asleep.rules.length" class="mute" style="font-size: var(--t-sm)">
              wakes on a request to
              <span v-for="h in dep.detail.asleep.hosts" :key="h" class="mono" style="margin-right: 8px">{{ h }}</span>
              <span v-for="r in dep.detail.asleep.rules" :key="r" class="mono" style="margin-right: 8px">/{{ r }}/</span>
            </div>
            <div v-else class="mute" style="font-size: var(--t-sm)">
              no hostnames — only Wake brings it back
            </div>
          </span>
        </li>
        <li v-if="dep.detail.compose">
          <span class="k">Overlays</span>
          <span class="v">
            <span v-for="o in dep.detail.compose.overlays" :key="o" class="mono" style="margin-right: 8px">{{ o }}</span>
            <span v-if="!dep.detail.compose.overlays.length" class="mute">none</span>
          </span>
        </li>
        <li>
          <span class="k">Axes</span>
          <span class="v">
            {{ dep.detail.axes.length }}
            <RouterLink v-if="dep.detail.axes.length" :to="`/deployments/${encodeURIComponent(dep.id)}/axes`">
              view →
            </RouterLink>
          </span>
        </li>
        <li>
          <span class="k">Requires</span>
          <span class="v">{{ dep.detail.requires.length }}</span>
        </li>
        <li><span class="k">Created</span><span class="v">{{ stamp(dep.detail.createdAt) }}</span></li>
        <li><span class="k">Updated</span><span class="v">{{ stamp(dep.detail.updatedAt) }}</span></li>
      </ul>
    </template>
  </section>
</template>
