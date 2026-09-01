<script setup lang="ts">
/**
 * Stored specs: what is on this host, and who is using each one.
 *
 * The "used by" count is computed from the deployments already in shared state rather than asked
 * for: every row carries `specName`, so the join is free. It matters because deleting a spec that
 * deployments still reference is refused — a deployment that cannot resolve its spec can never be
 * torn down — so knowing the count before you try is the difference between a plan and a 409.
 *
 * A server built before named specs answers 404 here. That is a capability difference, not an
 * error, so it gets its own plain banner instead of a red one.
 */
import { computed, ref } from 'vue';
import { sentence } from '../composables/useFormat';
import { api, problem } from '../api/client';
import type { SpecMeta, SpecsResponse } from '../api/types';
import { state, loadDeployments } from '../composables/useControlPlane';

import SkeletonList from '../components/SkeletonList.vue';
import ErrorNote from '../components/ErrorNote.vue';
import InfoHint from '../components/InfoHint.vue';
import RelativeTime from '../components/RelativeTime.vue';
import EquivalentCommand from '../components/EquivalentCommand.vue';
import RefreshButton from '../components/RefreshButton.vue';

const specs = ref<SpecMeta[]>([]);
const loaded = ref(false);
const error = ref('');
const unsupported = ref(false);
const needle = ref('');

async function load(): Promise<void> {
  const r = await api.get<SpecsResponse>('/api/specs');
  loaded.value = true;
  if (r.status === 404) {
    unsupported.value = true;
    return;
  }
  if (!r.ok) {
    error.value = problem(r, 'load specs');
    return;
  }
  error.value = '';
  specs.value = r.body.specs ?? [];
}

void load();
// The deployment list is what the "used by" column joins against; a deep link straight here may
// arrive before the shell's poll has filled it.
if (!state.deploymentsLoaded) void loadDeployments();

/** Deployment ids referencing each spec, by spec name. */
const users = computed(() => {
  const m = new Map<string, string[]>();
  for (const d of state.deployments) {
    if (!d.specName) continue;
    const list = m.get(d.specName) ?? [];
    list.push(d.id);
    m.set(d.specName, list);
  }
  return m;
});

const shown = computed(() => {
  const q = needle.value.trim().toLowerCase();
  if (!q) return specs.value;
  return specs.value.filter(
    (s) => s.name.toLowerCase().includes(q) || (s.description ?? '').toLowerCase().includes(q),
  );
});
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Specs</h1>
        <div class="sub">Stored once, used by any number of deployments</div>
      </div>
      <span class="grow" />
      <RefreshButton :run="load" />
    </div>

    <ErrorNote v-if="error" :text="error" title="Could not load the specs." />

    <!-- Not an empty list: this host cannot store specs at all (404, an older build). The two
         states have to stay tellable apart, so this banner is not the empty state below. -->
    <section v-if="unsupported" class="panel">
      <div class="banner plain">
        <b>This host is too old to store specs.</b>
      </div>
    </section>

    <section v-else class="panel">
      <SkeletonList v-if="!loaded" :rows="3" />

      <template v-else-if="specs.length">
        <div class="field" style="max-width: 320px; margin-bottom: var(--s4)">
          <label for="q">Search</label>
          <input id="q" v-model="needle" type="search" placeholder="name or description" />
        </div>

        <table class="cards" data-testid="specs.list">
          <thead>
            <tr>
              <th>Name</th>
              <th>Kind</th>
              <th>
                needs
                <InfoHint label="what needs means">
                  Variables the spec uses but does not set.
                </InfoHint>
              </th>
              <th>Used by</th>
              <th>Updated</th>
            </tr>
          </thead>
          <tbody class="stagger">
            <tr v-for="(s, i) in shown" :key="s.name" :style="{ '--i': i }">
              <td class="name" data-label="name">
                <RouterLink :to="`/specs/${encodeURIComponent(s.name)}`">{{ s.name }}</RouterLink>
                <div v-if="s.description" class="mute" style="font-size: var(--t-sm)">
                  {{ s.description }}
                </div>
              </td>
              <td data-label="kind">
                <span class="badge" :class="s.kind">{{ sentence(s.kind) }}</span>
              </td>
              <td data-label="needs">
                <span v-if="s.requiredVars.length">{{ s.requiredVars.join(', ') }}</span>
                <span v-else class="mute">none</span>
              </td>
              <td data-label="used by">
                <span v-if="users.get(s.name)?.length">{{ users.get(s.name)!.length }}</span>
                <span v-else class="mute">none</span>
              </td>
              <td class="dim nowrap" data-label="updated"><RelativeTime :at="s.updatedAt" /></td>
            </tr>
            <tr v-if="!shown.length">
              <td colspan="5" class="mute">No spec matches “{{ needle }}”.</td>
            </tr>
          </tbody>
        </table>
      </template>

      <!--
        Honest empty state. This page cannot create a spec yet, so pointing at "New spec" would be a
        dead end — the generated command below is the way that does work.
      -->
      <div v-else class="banner plain">
        <b>No specs yet — store one below.</b>
        <EquivalentCommand
          what="storing a spec"
          method="PUT"
          path="/api/specs/my-spec"
          :body="{ spec: '…the contents of your preview.yml…' }"
          cli="pstack validate -f preview.yml   # then PUT it with the curl above"
        />
      </div>
    </section>
  </div>
</template>
