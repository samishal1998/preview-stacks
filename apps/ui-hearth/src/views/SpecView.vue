<script setup lang="ts">
/**
 * One stored spec. The source is a restricted read: the server sends it only to an authenticated
 * request and says `sourceWithheld` otherwise, which is not the same as an empty spec.
 */
import { computed, reactive, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HCodeBlock,
  HDescriptionList,
  HEmptyState,
  HInput,
  HPageHeader,
  HSkeleton,
  HStack,
} from '@hearth-ui/vue';
import { deleteSpec, getSpec, putSpec, type SpecDetail } from '../api-specs';
import { stamp } from '../format';
import { usersOf } from '../lib/specs';
import MonoTextarea from '../components/MonoTextarea.vue';
import VerbatimAlert from '../components/VerbatimAlert.vue';
import { problem } from '../lib/problem';
import { can } from '../lib/users';
import { auth, data, toast } from '../state';

const props = defineProps<{ name: string }>();
const router = useRouter();

const spec = ref<SpecDetail | null>(null);
const loading = ref(true);
const error = ref('');
const missing = ref(false);

async function load(): Promise<void> {
  loading.value = true;
  missing.value = false;
  error.value = '';
  const want = props.name;
  const r = await getSpec(want);
  if (want !== props.name) return;
  loading.value = false;
  if (r.status === 404) {
    missing.value = true;
    spec.value = null;
    return;
  }
  if (!r.ok) {
    error.value = problem(r, 'Load spec');
    spec.value = null;
    return;
  }
  spec.value = r.body;
}

const users = computed(() => usersOf(data.deployments, props.name));
const usage = computed(() => (!data.loaded ? '—' : users.value.length ? users.value.join(', ') : 'none'));
const items = computed(() => [
  { key: 'needs', label: 'Needs', value: spec.value?.requiredVars.join(', ') || 'nothing' },
  { key: 'users', label: 'Used by', value: usage.value },
  { key: 'created', label: 'Created', value: stamp(spec.value?.createdAt) },
  { key: 'updated', label: 'Updated', value: stamp(spec.value?.updatedAt) },
]);

// ── edit ────────────────────────────────────────────────────────────────────────────────────────
const edit = reactive({ open: false, spec: '', compose: '', description: '', busy: false, error: '' });
function startEdit(): void {
  Object.assign(edit, { open: true, spec: spec.value?.source ?? '', compose: '', description: spec.value?.description ?? '', busy: false, error: '' });
}
async function save(): Promise<void> {
  edit.busy = true;
  edit.error = '';
  // Compose is not readable back; leaving it blank keeps the stored one.
  const body: { spec: string; compose?: string; description: string } = { spec: edit.spec, description: edit.description.trim() };
  if (edit.compose.trim()) body.compose = edit.compose;
  const r = await putSpec(props.name, body);
  edit.busy = false;
  if (!r.ok) {
    edit.error = problem(r, 'Save spec');
    return;
  }
  edit.open = false;
  toast(`Saved ${props.name}`, 'success');
  void load();
}

// ── delete ──────────────────────────────────────────────────────────────────────────────────────
const del = reactive({ open: false, busy: false, error: '' });
watch(() => props.name, () => { edit.open = false; del.open = false; void load(); }, { immediate: true });
async function remove(): Promise<void> {
  del.busy = true;
  const r = await deleteSpec(props.name);
  del.busy = false;
  if (!r.ok) {
    del.open = false; // the error alert sits behind the modal
    del.error = r.status === 409 && r.body.deployments?.length ? `Still used by ${r.body.deployments.join(', ')}.` : problem(r, 'Delete spec');
    return;
  }
  toast(`Deleted ${props.name}`, 'success');
  void router.push('/specs');
}
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader :title="name" :description="spec?.description ?? (missing ? 'Not found.' : '')" actions-placement="end">
      <template #actions>
        <HBadge v-if="spec" :label="spec.kind" :tone="spec.kind === 'shared' ? 'warning' : 'neutral'" variant="outline" />
        <HButton variant="secondary" icon="left" label="Specs" @click="router.push('/specs')" />
        <HButton v-if="spec && can('developer', auth)" variant="secondary" label="Use in a deployment" icon="plus" @click="router.push(`/submit?spec=${encodeURIComponent(name)}`)" />
        <HButton v-if="spec && can('developer', auth)" variant="secondary" label="Edit" :disabled="!!spec.sourceWithheld || edit.open" @click="startEdit" />
        <HButton
          v-if="spec && can('developer', auth)"
          variant="danger"
          label="Delete"
          :disabled="!data.loaded || users.length > 0"
          @click="del.error = ''; del.open = true"
        />
      </template>
    </HPageHeader>

    <HAlert v-if="error" tone="danger" title="Could not load this spec" :description="error" />
    <HEmptyState v-if="missing" :title="`No spec named ${name}`" icon="code">
      <HButton variant="secondary" label="Specs" @click="router.push('/specs')" />
    </HEmptyState>
    <HSkeleton v-else-if="loading" variant="rectangle" height="160px" />

    <template v-else-if="spec">
      <HAlert v-if="users.length" tone="info" compact :description="`Used by ${users.length} deployment${users.length === 1 ? '' : 's'}. Remove them to delete this spec.`" />
      <HDescriptionList :items="items" :columns="2" variant="inline">
        <template #value:users>
          <template v-if="users.length">
            <template v-for="(u, i) in users" :key="u">
              <RouterLink :to="`/deployments/${encodeURIComponent(u)}`">{{ u }}</RouterLink><span v-if="i < users.length - 1">, </span>
            </template>
          </template>
          <template v-else>{{ usage }}</template>
        </template>
      </HDescriptionList>

      <HAlert v-if="spec.sourceWithheld" tone="neutral" title="Source hidden" description="Sign in to see it.">
        <RouterLink to="/settings">Settings</RouterLink>
      </HAlert>

      <HStack v-else-if="edit.open" direction="column" :gap="3">
        <HInput v-model="edit.description" label="Description" />
        <MonoTextarea v-model="edit.spec" label="spec.yml" :rows="16" resize="vertical" />
        <MonoTextarea v-model="edit.compose" label="compose.yml" hint="Blank keeps the stored file." :rows="8" resize="vertical" />
        <VerbatimAlert v-if="edit.error" title="The server rejected this spec" :text="edit.error" />
        <HStack direction="row" :gap="2">
          <HButton variant="primary" label="Save" :loading="edit.busy" :disabled="!edit.spec.trim()" @click="save" />
          <HButton variant="ghost" label="Cancel" :disabled="edit.busy" @click="edit.open = false" />
        </HStack>
      </HStack>

      <HCodeBlock v-else-if="spec.source" :code="spec.source" title="spec.yml" language="yaml" line-numbers max-height="32rem" />
      <HEmptyState v-else title="No source" icon="code" />
    </template>

    <HAlert v-if="del.error" tone="danger" title="Not deleted" :description="del.error" dismissible @dismiss="del.error = ''" />
    <HAlertDialog
      v-model:open="del.open"
      danger
      :busy="del.busy"
      :title="`Delete ${name}?`"
      description="The stored spec is removed from this host."
      confirm-label="Delete"
      @confirm="remove"
    />
  </HStack>
</template>

