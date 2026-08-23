<script setup lang="ts">
/** The resolved spec, summarised. Read-only — everything that acts lives on Danger. */
import { computed } from 'vue';
import { dep, isShared, row } from '../../composables/useDeployment';
import { sentence, stamp } from '../../composables/useFormat';
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
    <p v-else-if="!dep.detail" class="mute">Unavailable until the spec resolves.</p>

    <template v-else>
      <ul class="kvlist">
        <li>
          <span class="k">
            Stack
            <InfoHint label="the difference between id and stack">
              The <b>id</b> is what you named this deployment. The <b>stack</b> is what its spec
              resolved to once the variables were filled in — the compose project name, the hostname
              label, and <code>$STACK</code> inside every hook.
            </InfoHint>
          </span>
          <span class="v mono">{{ dep.detail.stack || '—' }}</span>
        </li>
        <li>
          <span class="k">Kind</span>
          <span class="v">
            <span class="badge" :class="dep.detail.kind">{{ sentence(dep.detail.kind) }}</span>
            <span class="mute" style="margin-left: 8px; font-size: var(--t-sm)">{{ kindBlurb }}</span>
          </span>
        </li>
        <li v-if="row?.specName">
          <span class="k">Spec</span>
          <span class="v">
            <b>{{ row.specName }}</b>
            <span class="mute" style="font-size: var(--t-sm)">
              — stored, and shared with any other deployment using it
            </span>
          </span>
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
            <span v-if="!dep.detail.compose.profiles.length" class="mute">
              none — a bare up starts every service
            </span>
          </span>
        </li>
        <li v-if="dep.detail.compose?.subdomains?.length">
          <span class="k">
            Subdomains
            <InfoHint label="how wildcard subdomains work">
              Anything one label under these hosts reaches the same service the bare host does.
              An exact hostname you route yourself always wins over the wildcard.
              <template v-if="dep.detail.compose.subdomains.some((s) => s.depth === 'any')">
                A route marked <b>any depth</b> works over HTTP only — no TLS certificate can cover
                more than one label.
              </template>
            </InfoHint>
          </span>
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
          <span class="k">
            Orchestrator
            <InfoHint label="compose or swarm">
              <b>compose</b> runs the project with docker compose on this host. <b>swarm</b> deploys
              it as a swarm stack — the file you submitted is converted on every deploy (profiles
              resolved, <code>restart</code> and limits moved under <code>deploy</code>, Traefik
              labels onto the service), and its tasks may land on any node of the swarm.
            </InfoHint>
          </span>
          <span class="v">{{ dep.detail.orchestrator ?? 'compose' }}</span>
        </li>
        <li v-if="dep.detail.sleep">
          <span class="k">
            Sleep policy
            <InfoHint label="what the scheduler does">
              The scheduler takes the compose project down — volumes and axes stay — when the policy
              says so, and the next request to any of its hostnames brings it back. <b>idle</b> counts
              requests through Traefik; <b>after</b> counts from the last deploy.
            </InfoHint>
          </span>
          <span class="v">
            <span v-if="dep.detail.sleep.idle">after <b>{{ dep.detail.sleep.idle }}</b> without a request</span>
            <span v-if="dep.detail.sleep.idle && dep.detail.sleep.after" class="mute"> · </span>
            <span v-if="dep.detail.sleep.after"><b>{{ dep.detail.sleep.after }}</b> after the last deploy</span>
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
              no hostnames were found on its containers — only Wake brings it back
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
