<script setup lang="ts">
/**
 * The deployment list: searchable, filterable, and honest about what could not be resolved.
 *
 * Filtering is client-side because the API has no query parameters for it and the list is one
 * host's worth of previews — a few dozen rows, already in memory from the shell's poll.
 */
import { computed, reactive, ref } from 'vue';
import { Search } from 'lucide-vue-next';
import { sentence } from '../composables/useFormat';
import { loadDeployments, state, summary } from '../composables/useControlPlane';
import { bulk, runBulk } from '../composables/useBulkActions';
import { BULK_LABELS, skipReason, type BulkAction } from '../composables/bulkRules';

import RunStateBadge from '../components/RunStateBadge.vue';
import SkeletonList from '../components/SkeletonList.vue';
import RelativeTime from '../components/RelativeTime.vue';
import SelectMenu from '../components/SelectMenu.vue';
import RefreshButton from '../components/RefreshButton.vue';
import ActionButton from '../components/ActionButton.vue';

const q = ref('');
const kind = ref<'all' | 'isolated' | 'shared'>('all');
const onlyLive = ref(false);

const rows = computed(() => {
  const needle = q.value.trim().toLowerCase();
  return state.deployments.filter((d) => {
    if (kind.value !== 'all' && d.kind !== kind.value) return false;
    // "Live" means DEMONSTRABLY live. An unknown is not evidence of either state, so it is not
    // silently included here — the filter says what it filters.
    // Asleep counts as live: it is deliberately down and comes back on a request — not torn down.
    if (onlyLive.value && !(d.running === true || d.busy === true || d.asleep)) return false;
    if (!needle) return true;
    return (
      d.id.toLowerCase().includes(needle) ||
      (d.stack ?? '').toLowerCase().includes(needle) ||
      (d.specName ?? '').toLowerCase().includes(needle)
    );
  });
});

const unresolvedRows = computed(() => state.deployments.filter((d) => d.unresolved));

/*
 * Selection acts on what is SHOWN: a row hidden by the filter is never acted on, even if it was
 * ticked before the filter changed. The bar counts the same rows the actions will send.
 */
const selected = reactive(new Set<string>());
const chosen = computed(() => rows.value.filter((d) => selected.has(d.id)));
const allShown = computed(() => rows.value.length > 0 && chosen.value.length === rows.value.length);
function toggleAll(): void {
  if (allShown.value) selected.clear();
  else rows.value.forEach((d) => selected.add(d.id));
}
const sendable = (a: BulkAction) => chosen.value.filter((d) => !skipReason(d, a)).length;
const sharedChosen = computed(() => chosen.value.filter((d) => d.kind === 'shared').length);

async function run(a: BulkAction): Promise<void> {
  const targets = chosen.value;
  selected.clear();
  await runBulk(targets, a);
}
const plain: BulkAction[] = ['up', 'verify', 'sleep', 'wake'];
const destructive: BulkAction[] = ['down', 'down-forget'];
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Deployments</h1>
        <div class="sub">
          {{ summary.total }} submitted · {{ summary.running }} running · {{ summary.busy }} busy
        </div>
      </div>
      <span class="grow" />
      <RefreshButton :run="loadDeployments" />
      <RouterLink to="/submit" class="btn">+ Submit</RouterLink>
    </div>

    <section class="panel">
      <div class="phead">
        <!--
          One row of equal-height controls. Each used to sit under its own stacked label, which cost
          a line of vertical space per control, put "Search — press /" on screen as a permanent
          instruction, and left the two fields 8px out of vertical alignment because a `select` and
          an `input` do not have the same intrinsic height. A placeholder says the same thing in the
          place you are already looking, and the select's own options say what it filters.
        -->
        <div class="searchbox">
          <Search :size="16" aria-hidden="true" />
          <input
            id="q"
            v-model="q"
            data-search
            type="search"
            aria-label="Search deployments"
            placeholder="Search by id, stack or spec"
            spellcheck="false"
          />
          <kbd v-if="!q">/</kbd>
        </div>
        <SelectMenu
          v-model="kind"
          label="Filter by kind"
          :options="[
            { value: 'all', label: 'All kinds' },
            { value: 'isolated', label: 'Isolated' },
            { value: 'shared', label: 'Shared' },
          ]"
        />
        <label class="check">
          <input v-model="onlyLive" type="checkbox" />
          Running or busy only
        </label>
      </div>

      <p v-if="state.deploymentsError" class="s-failed">{{ state.deploymentsError }}</p>
      <SkeletonList v-else-if="!state.deploymentsLoaded" :rows="4" tall />

      <template v-else>
        <div v-if="chosen.length || bulk.running" class="bulkbar row">
          <b>{{ chosen.length }} selected</b>
          <button v-if="!allShown" class="ghost sm" @click="toggleAll">Select all {{ rows.length }}</button>
          <button class="ghost sm" @click="selected.clear()">Clear</button>
          <span v-if="sharedChosen" class="mute">{{ sharedChosen }} shared — skipped by tear down</span>
          <span class="grow" />
          <ActionButton
            v-for="a in plain"
            :key="a"
            :pending="bulk.running && bulk.action === a"
            :disabled="bulk.running || !sendable(a)"
            :title="sendable(a) ? undefined : `None of the selected can ${BULK_LABELS[a].toLowerCase()}.`"
            @click="run(a)"
          >
            {{ BULK_LABELS[a] }}
          </ActionButton>
          <ActionButton
            v-for="a in destructive"
            :key="a"
            variant="danger"
            :pending="bulk.running && bulk.action === a"
            :disabled="bulk.running || !sendable(a)"
            :title="sendable(a) ? undefined : 'None of the selected can be torn down here.'"
            :confirm="`${BULK_LABELS[a]} ${sendable(a)}?`"
            @run="run(a)"
          >
            {{ BULK_LABELS[a] }}
          </ActionButton>
        </div>

        <div v-if="bulk.results.length && bulk.action" class="banner bulkresults">
          <div class="row">
            <b>{{ BULK_LABELS[bulk.action] }}</b>
            <span class="grow" />
            <button class="ghost sm" @click="bulk.results = []">Dismiss</button>
          </div>
          <ul>
            <li v-for="r in bulk.results" :key="r.id">
              <span class="mono">{{ r.id }}</span> —
              <RouterLink v-if="r.job" :to="`/jobs/${encodeURIComponent(r.job.id)}`">{{ r.job.state }}</RouterLink>
              <span v-else-if="r.error" class="s-failed">{{ r.error }}</span>
              <span v-else class="mute">skipped: {{ r.skipped }}</span>
            </li>
          </ul>
        </div>

        <table role="table" class="cards">
          <thead role="rowgroup">
            <tr role="row">
              <th role="columnheader" class="pick">
                <input
                  type="checkbox"
                  aria-label="Select all shown"
                  :checked="allShown"
                  :indeterminate="chosen.length > 0 && !allShown"
                  @change="toggleAll"
                />
              </th>
              <th role="columnheader">ID</th>
              <th role="columnheader">Kind</th>
              <th role="columnheader">Stack</th>
              <th role="columnheader">State</th>
              <th role="columnheader">Updated</th>
            </tr>
          </thead>
          <tbody role="rowgroup" class="stagger">
            <tr v-for="(d, i) in rows" :key="d.id" role="row" :style="{ '--i': i }">
              <td role="cell" class="pick" data-label="select">
                <input
                  type="checkbox"
                  :aria-label="`Select ${d.id}`"
                  :checked="selected.has(d.id)"
                  @change="selected.has(d.id) ? selected.delete(d.id) : selected.add(d.id)"
                />
              </td>
              <td role="cell" data-label="id">
                <RouterLink :to="`/deployments/${encodeURIComponent(d.id)}`">
                  {{ d.id }}
                </RouterLink>
                <div v-if="d.specName" class="mute" style="font-size: var(--t-xs)">
                  spec: {{ d.specName }}
                </div>
              </td>
              <td role="cell" data-label="kind"><span class="badge" :class="d.kind">{{ sentence(d.kind) }}</span></td>
              <td role="cell" class="name dim" data-label="stack">
                <span v-if="d.stack">{{ d.stack }}</span>
                <!--
                  No stack name means the spec could not be resolved with the variables this
                  listing had. It is not an error state for the deployment — it is a missing input.
                -->
                <span v-else class="badge warn" title="the spec could not be resolved without variables">
                  Needs variables
                </span>
              </td>
              <td role="cell" data-label="state"><RunStateBadge :busy="d.busy" :running="d.running" :asleep="d.asleep" /></td>
              <td role="cell" class="dim nowrap" data-label="updated"><RelativeTime :at="d.updatedAt" /></td>
            </tr>
            <tr v-if="!rows.length" role="row">
              <td role="cell" colspan="6" class="mute">
                <template v-if="state.deployments.length">
                  Nothing matches this filter.
                  <button class="ghost sm" @click="q = ''; kind = 'all'; onlyLive = false">
                    Clear filters
                  </button>
                </template>
                <template v-else>
                  Nothing submitted yet — <RouterLink to="/submit">submit a spec</RouterLink> to
                  put a deployment in the registry.
                </template>
              </td>
            </tr>
          </tbody>
        </table>

        <!-- One message per unresolved row, verbatim: it names the missing variable. -->
        <div v-for="d in unresolvedRows" :key="`u-${d.id}`" class="banner warn">
          <b>{{ d.id }}</b> could not be resolved:
          <span class="mono">{{ d.unresolved }}</span>
          <p>
            <RouterLink :to="`/deployments/${encodeURIComponent(d.id)}/config`">
              Set its variables
            </RouterLink>
          </p>
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
.pick {
  width: 2rem;
}
.bulkbar {
  padding: var(--s2) 0;
  margin-bottom: var(--s2);
  border-bottom: 1px solid var(--line);
}
.bulkresults {
  max-width: none;
}
.bulkresults ul {
  margin: var(--s2) 0 0;
  padding-left: var(--s4);
}
</style>
