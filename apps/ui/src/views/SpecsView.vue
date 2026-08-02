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
 * error, so it gets its own explanation instead of a red banner.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import type { SpecMeta, SpecsResponse } from '../api/types';
import { state, loadDeployments } from '../composables/useControlPlane';

import SkeletonList from '../components/SkeletonList.vue';
import ErrorNote from '../components/ErrorNote.vue';
import InfoHint from '../components/InfoHint.vue';
import RelativeTime from '../components/RelativeTime.vue';

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
        <div class="sub">
          Stored once, used by any number of deployments
          <InfoHint label="why store a spec">
            Without this, every deployment carries its own copy — 50 open pull requests meant 50
            identical files, and fixing a teardown step meant re-submitting it 50 times.
          </InfoHint>
        </div>
      </div>
    </div>

    <ErrorNote v-if="error" :text="error" title="Could not load the specs." />

    <section v-if="unsupported" class="panel">
      <div class="banner plain">
        <b>This server has no stored specs.</b>
        <p>
          It is an older build of pstack — every deployment carries its own copy of its spec, which
          still works. Upgrade the host to store specs and reference them by name.
        </p>
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
              <th>name</th>
              <th>kind</th>
              <th>
                needs
                <InfoHint label="what needs means">
                  Variables the spec uses but does not set. Every deployment referencing it must
                  supply these, which is how one spec serves many stacks.
                </InfoHint>
              </th>
              <th>used by</th>
              <th>updated</th>
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
                <span class="badge" :class="s.kind">{{ s.kind }}</span>
              </td>
              <td data-label="needs">
                <span v-if="s.requiredVars.length">{{ s.requiredVars.join(', ') }}</span>
                <span v-else class="mute">nothing</span>
              </td>
              <td data-label="used by">
                <span v-if="users.get(s.name)?.length">
                  {{ users.get(s.name)!.length }} deployment(s)
                </span>
                <span v-else class="mute">nothing yet</span>
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
        dead end — it names the way that does work instead.
      -->
      <div v-else class="banner plain">
        <b>No specs stored yet.</b>
        <p>
          A deployment can carry its own spec inline — see
          <RouterLink to="/submit">Submit</RouterLink> — and that is all you need for a one-off.
          Storing one here pays off when several deployments share it.
        </p>
        <p class="mute">
          Storing is not yet possible from this page; send the spec to
          <code>PUT /api/specs/&lt;name&gt;</code> with your token.
        </p>
      </div>
    </section>
  </div>
</template>
