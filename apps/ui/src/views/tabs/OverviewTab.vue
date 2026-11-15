<script setup lang="ts">
/**
 * The resolved spec, summarised, read-only. The one write here is each routed service's wildcard
 * subdomains, which the spec may leave to the host; it applies on the next deploy, so it never
 * deploys itself.
 */
import { computed, ref, watch } from 'vue';
import { api, problem } from '../../api/client';
import type { RoutedService } from '../../api/types';
import { can } from '../../composables/useAuth';
import { depthEditable, depthLabel, depthOptions, toDepth } from '../../composables/customRoutes';
import { dep, row } from '../../composables/useDeployment';
import { sentence, stamp } from '../../composables/useFormat';
import { toast } from '../../composables/useToasts';
import SelectMenu from '../../components/SelectMenu.vue';
import SkeletonList from '../../components/SkeletonList.vue';

const routed = ref<RoutedService[]>([]);
/** The service whose wildcard is saving. */
const saving = ref('');
const mayWrite = computed(() => can('developer'));
const subPath = (): string => `/api/deployments/${encodeURIComponent(dep.id)}/subdomains`;

// ponytail: a failed read hides the section (as an older server's 404 should); surface it if that hides real faults.
async function loadRouted(): Promise<void> {
  const r = await api.get<RoutedService[]>(subPath());
  routed.value = r.ok && Array.isArray(r.body) ? r.body : [];
}
watch(() => dep.id, () => void loadRouted(), { immediate: true });

async function setDepth(s: RoutedService, v: string): Promise<void> {
  const depth = toDepth(v);
  if (depth === s.subdomains || saving.value) return;
  saving.value = s.service;
  const r = await api.put<{ note?: string }>(subPath(), { service: s.service, depth });
  saving.value = '';
  // The server's 409 already names the service.
  if (r.ok) toast('ok', r.body.note ?? 'Applies on the next deploy.');
  else toast('error', problem(r, 'set subdomains'));
  void loadRouted();
}
</script>

<template>
  <div>
  <section class="panel">
    <div class="phead">
      <h2 class="section">Resolved spec</h2>
      <span class="grow" />
      <!-- One flex item, so the separator never ends a line on its own. -->
      <span v-if="dep.detail">
        <RouterLink :to="`/submit?from=${encodeURIComponent(dep.id)}`">Duplicate</RouterLink>
        <span class="mute"> · </span>
        <RouterLink :to="`/submit/${encodeURIComponent(dep.id)}`">Edit spec &amp; compose&nbsp;→</RouterLink>
      </span>
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
            <span v-for="p in dep.detail.compose.profiles" :key="p" class="badge" style="margin-right: var(--s1)">
              {{ p }}
            </span>
            <span v-if="!dep.detail.compose.profiles.length" class="mute">none</span>
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
              <span v-for="h in dep.detail.asleep.hosts" :key="h" class="mono" style="display: inline-block; margin-right: var(--s2)">{{ h }}</span>
              <span v-for="r in dep.detail.asleep.rules" :key="r" class="mono" style="display: inline-block; margin-right: var(--s2)">/{{ r }}/</span>
            </div>
            <div v-else class="mute" style="font-size: var(--t-sm)">
              no hostnames — only Wake brings it back
            </div>
          </span>
        </li>
        <li v-if="dep.detail.compose">
          <span class="k">Overlays</span>
          <span class="v">
            <span v-for="o in dep.detail.compose.overlays" :key="o" class="mono" style="margin-right: var(--s2)">{{ o }}</span>
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

  <section v-if="dep.detail?.compose && routed.length" class="panel">
    <div class="phead">
      <h2 class="section">Subdomains</h2>
    </div>
    <div class="table-scroll">
      <table role="table" class="cards tbl-fixed">
        <colgroup>
          <col style="width: 20%" />
          <col />
          <col style="width: 14rem" />
        </colgroup>
        <thead role="rowgroup">
          <tr role="row">
            <th role="columnheader">Service</th>
            <th role="columnheader">Host</th>
            <th role="columnheader">Subdomains</th>
          </tr>
        </thead>
        <tbody role="rowgroup">
          <tr v-for="s in routed" :key="s.service" role="row">
            <td role="cell" class="mono cell-wrap" data-label="service">{{ s.service }}</td>
            <td role="cell" class="mono cell-wrap" data-label="host">{{ s.host }}</td>
            <td role="cell" data-label="subdomains">
              <SelectMenu
                v-if="depthEditable(s.from, mayWrite)"
                :model-value="s.subdomains ?? ''"
                :disabled="saving !== ''"
                :label="`Subdomains for ${s.service}`"
                :options="depthOptions"
                @update:model-value="(v) => setDepth(s, v)"
              />
              <span v-else>
                {{ depthLabel(s.subdomains) }}<span v-if="s.from === 'spec'" class="mute"> · Set in spec.</span>
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
  </div>
</template>
