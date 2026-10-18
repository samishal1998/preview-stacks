<script setup lang="ts">
/**
 * Stored specs on this host and how many deployments use each. A 404 is a build without named
 * specs, which is not an error and not an empty list.
 */
import { computed, onMounted, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlert,
  HBadge,
  HButton,
  HDataTable,
  HDialog,
  HEmptyState,
  HInput,
  HPageHeader,
  HStack,
  HFilterBar,
  type TableColumn,
  type TableRow,
  type TableSort,
} from '@hearth-ui/vue';
import MonoTextarea from '../components/MonoTextarea.vue';
import VerbatimAlert from '../components/VerbatimAlert.vue';
import { listSpecs, putSpec, type SpecMeta } from '../api-specs';
import { ago } from '../format';
import { filterSpecs, usersOf, validName } from '../lib/specs';
import { problem } from '../lib/problem';
import { can } from '../lib/users';
import { auth, data, toast } from '../state';

const router = useRouter();
const specs = ref<SpecMeta[]>([]);
const loaded = ref(false);
const refreshing = ref(false);
const error = ref('');
const unsupported = ref(false);
const q = ref('');
const sort = ref<TableSort>({ key: 'name', direction: 'ascending' });

async function load(): Promise<void> {
  refreshing.value = true;
  const r = await listSpecs();
  refreshing.value = false;
  loaded.value = true;
  if (r.status === 404) {
    unsupported.value = true;
    return;
  }
  if (!r.ok) {
    error.value = problem(r, 'Load specs');
    specs.value = [];
    return;
  }
  error.value = '';
  specs.value = r.body.specs ?? [];
}
onMounted(load);

const count = (s: SpecMeta): number => usersOf(data.deployments, s.name).length;
const usedCell = (s: SpecMeta): string | number => (data.loaded ? count(s) : '—');
const columns: TableColumn[] = [
  { key: 'name', label: 'Name', sortable: true, truncate: true },
  { key: 'kind', label: 'Kind', sortable: true, hideBelow: 480 },
  { key: 'needs', label: 'Needs', truncate: true, hideBelow: 680 },
  { key: 'used', label: 'Used by', sortable: true, align: 'end' },
  { key: 'updated', label: 'Updated', sortable: true, align: 'end', hideBelow: 820 },
];
const rows = computed<TableRow[]>(() => {
  const dir = sort.value.direction === 'ascending' ? 1 : -1;
  const key = sort.value.key;
  const val = (s: SpecMeta): string | number =>
    key === 'used' ? count(s) : key === 'updated' ? s.updatedAt : key === 'kind' ? s.kind : s.name;
  return [...filterSpecs(specs.value, q.value)]
    .sort((a, b) => (val(a) < val(b) ? -dir : val(a) > val(b) ? dir : 0))
    .map((s) => ({
      id: s.name,
      name: s.name,
      kind: s.kind,
      needs: s.requiredVars.join(', ') || 'none',
      used: usedCell(s),
      updated: ago(s.updatedAt),
    }));
});

// ── new spec ────────────────────────────────────────────────────────────────────────────────────
const draft = reactive({ open: false, name: '', description: '', spec: '', compose: '', busy: false, error: '' });
const nameError = computed(() => {
  if (!draft.name) return undefined;
  if (!validName(draft.name)) return 'Lower case letters, digits, . _ - (max 64).';
  return specs.value.some((s) => s.name === draft.name) ? 'Already exists. Open it to edit.' : undefined;
});
function openNew(): void {
  Object.assign(draft, { open: true, name: '', description: '', spec: '', compose: '', busy: false, error: '' });
}
async function create(): Promise<void> {
  draft.busy = true;
  draft.error = '';
  const body: { spec: string; description?: string; compose?: string } = { spec: draft.spec };
  if (draft.compose.trim()) body.compose = draft.compose;
  if (draft.description.trim()) body.description = draft.description.trim();
  const r = await putSpec(draft.name, body);
  draft.busy = false;
  if (!r.ok) {
    draft.error = problem(r, 'Save spec');
    return;
  }
  toast(`Created ${draft.name}`, 'success');
  draft.open = false;
  void router.push(`/specs/${encodeURIComponent(draft.name)}`);
}
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Specs" :description="loaded && !error ? `${specs.length} stored on this host.` : undefined" actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" :loading="refreshing" @click="load" />
        <HButton v-if="!unsupported && can('developer', auth)" variant="primary" icon="plus" label="New spec" @click="openNew" />
      </template>
    </HPageHeader>

    <HAlert v-if="error" tone="danger" title="Could not load specs" :description="error" />
    <HEmptyState v-if="unsupported" title="No stored specs" description="This host is too old to store specs." icon="code" />

    <template v-else>
      <HFilterBar label="Filters" :active-count="q ? 1 : 0" @clear="q = ''">
        <HInput v-model="q" label="Search" hide-label type="search" placeholder="Search specs" leading-icon="search" clearable />
      </HFilterBar>
      <HDataTable v-if="!error"
        label="Stored specs"
        :rows="rows"
        :columns="columns"
        :loading="!loaded || refreshing"
        :sort="sort"
        :page-size="15"
        row-activatable
        :empty-text="specs.length ? 'No spec matches.' : can('developer', auth) ? 'No specs yet. Use New spec.' : 'No specs yet.'"
        @sort-change="sort = $event"
        @row-activate="router.push(`/specs/${encodeURIComponent(String($event.id))}`)"
      >
        <template #cell="{ column, value }">
          <HBadge v-if="column.key === 'kind'" :label="String(value)" :tone="value === 'shared' ? 'warning' : 'neutral'" variant="outline" />
          <template v-else>{{ value }}</template>
        </template>
      </HDataTable>
    </template>

    <HDialog :open="draft.open" title="New spec" @close="draft.open = false">
      <HStack direction="column" :gap="3">
        <HInput v-model="draft.name" label="Name" placeholder="web" autocomplete="off" :error="nameError" />
        <HInput v-model="draft.description" label="Description" hint="Optional." />
        <MonoTextarea v-model="draft.spec" label="spec.yml" :rows="12" resize="vertical" />
        <MonoTextarea v-model="draft.compose" label="compose.yml" hint="Optional." :rows="6" resize="vertical" />
        <VerbatimAlert v-if="draft.error" title="The server rejected this spec" :text="draft.error" />
      </HStack>
      <template #footer>
        <HButton variant="ghost" label="Cancel" :disabled="draft.busy" @click="draft.open = false" />
        <HButton variant="primary" label="Save" :loading="draft.busy" :disabled="!validName(draft.name) || !!nameError || !draft.spec.trim()" @click="create" />
      </template>
    </HDialog>
  </HStack>
</template>

