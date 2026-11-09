<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import {
  HAlert,
  HAlertDialog,
  HButton,
  HCard,
  HDataTable,
  HDescriptionList,
  HInput,
  HNumberInput,
  HSelect,
  HSettingsPage,
  HStack,
  HText,
  type TableColumn,
  type TableRow,
} from '@hearth-ui/vue';
import MonoTextarea from '../../components/MonoTextarea.vue';
import { controlApi, poll, why, type ScalerPreset, type ScalerSettings, type ScalerView } from '../../api-control';
import { ago, sentence, stamp } from '../../format';
import { openOrphans, scalerBody, scalerLocked, scalerShown } from '../../lib/control';
import { toast } from '../../state';

defineProps<{ canAdmin: boolean }>();

const view = ref<ScalerView | null>(null);
const presets = ref<ScalerPreset[]>([]);
const loaded = ref(false);
const loadError = ref('');
const saveError = ref('');
const saving = ref(false);

const blank = (): ScalerSettings => ({ preset: '', vars: {}, min: 0, max: 3, cooldownMin: 5, graceMin: 15, dismissed: [] });
const draft = ref<ScalerSettings>(blank());
const creds = ref<Record<string, string>>({});
const newKey = ref('');
const newValue = ref('');
const saved = computed(() => view.value?.settings ?? blank());
const savedKey = (s: ScalerSettings) => JSON.stringify([s.preset, s.vars, s.min, s.max, s.cooldownMin, s.graceMin]);

// Refill only when the SAVED values change: the poll must not wipe typing (a dismissal is not one).
watch(
  () => view.value && savedKey(view.value.settings),
  () => {
    if (!view.value) return;
    draft.value = { ...view.value.settings, vars: { ...view.value.settings.vars } };
    creds.value = {};
  },
);

async function load(): Promise<void> {
  const [r, p] = await Promise.all([controlApi.scaler(), controlApi.presets()]);
  loaded.value = true;
  if (!r.ok) {
    loadError.value = r.status === 403 ? 'Maintainer only.' : why(r, 'Read the scaler');
    return;
  }
  loadError.value = '';
  view.value = r.body;
  if (p.ok) presets.value = p.body;
}
poll(load, 10_000);

const report = computed(() => view.value?.report ?? null);
const locked = computed(() => scalerLocked(report.value));
const presetOf = (name: string) => presets.value.find((p) => p.name === name);
const chosen = computed(() => presetOf(draft.value.preset));
const vars = computed(() => [
  ...(chosen.value?.preset.perWorker ?? []).map((name) => ({ name, shared: false })),
  ...(chosen.value?.preset.shared ?? []).map((name) => ({ name, shared: true })),
]);
const presetOptions = computed(() => presets.value.map((p) => ({ value: p.name, label: p.builtin ? p.name : `${p.name} (custom)` })));
const credKeys = computed(() => Object.keys(view.value?.credentials ?? {}).sort());
const orphans = computed(() => (view.value ? openOrphans(view.value) : []));

const whole = (v: number | null) => (v === null || !Number.isInteger(v) || v < 0 ? 'A whole number.' : '');
const problems = computed(() => {
  const d = draft.value;
  return {
    min: whole(d.min),
    max: whole(d.max) || (d.max < d.min ? 'At least min.' : ''),
    cooldownMin: whole(d.cooldownMin),
    graceMin: whole(d.graceMin),
  };
});
const valid = computed(() => Object.values(problems.value).every((p) => !p) && !!draft.value.preset);
const typed = computed(() => ({ ...creds.value, ...(newKey.value.trim() ? { [newKey.value.trim()]: newValue.value } : {}) }));
const dirty = computed(() => savedKey(draft.value) !== savedKey(saved.value) || Object.values(typed.value).some((v) => v));

async function put(s: ScalerSettings, typedCreds: Record<string, string> = {}): Promise<boolean> {
  saving.value = true;
  const r = await controlApi.saveScaler(scalerBody(s, presetOf(s.preset)?.preset, report.value?.orphans ?? [], typedCreds));
  saving.value = false;
  if (!r.ok) {
    saveError.value = why(r, 'Save');
    return false;
  }
  saveError.value = '';
  view.value = r.body;
  return true;
}
async function save(): Promise<void> {
  if (!valid.value) {
    saveError.value = 'Fix the highlighted fields.';
    return;
  }
  if (!(await put(draft.value, typed.value))) return;
  creds.value = {};
  newKey.value = newValue.value = '';
  toast('Saved.', 'success');
}
const reset = () => {
  draft.value = { ...saved.value, vars: { ...saved.value.vars } };
  creds.value = {};
  newKey.value = newValue.value = '';
};

// ── owned workers, does-not-fit, orphans ─────────────────────────────────────────────────────────
const workerColumns: TableColumn[] = [
  { key: 'name', label: 'Name' },
  { key: 'settings', label: 'Settings', truncate: true, hideBelow: 560 },
  { key: 'state', label: 'State' },
  { key: 'created', label: 'Created', hideBelow: 480 },
];
const workerRows = computed<TableRow[]>(() =>
  (report.value?.workers ?? []).map((w) => ({
    id: w.name,
    name: w.name,
    settings: Object.entries(w.vars).map(([k, v]) => `${k}=${v}`).join(' '),
    state: sentence(w.state),
    created: ago(w.createdAt),
  })),
);
const facts = computed(() => {
  const r = report.value;
  const items = [{ key: 'host', label: 'Host ID', value: r?.hostId || '—' }];
  if (!r) return items;
  if (r.lastAction) items.push({ key: 'action', label: 'Last action', value: r.lastAction });
  if (r.lastError) items.push({ key: 'error', label: 'Last error', value: r.lastError });
  if (r.nextRetryAt) items.push({ key: 'retry', label: 'Next retry', value: stamp(r.nextRetryAt) });
  if (r.doesNotFit.length) items.push({ key: 'fit', label: 'Does not fit', value: r.doesNotFit.join(', ') });
  if (r.pendingDestroy) items.push({ key: 'destroy', label: 'Pending destroy', value: r.pendingDestroy });
  return items;
});
async function dismiss(name: string): Promise<void> {
  const s = saved.value;
  if (await put({ ...s, dismissed: [...s.dismissed, name] })) toast(`Dismissed ${name}.`, 'success');
}

// ── custom presets (admin) ───────────────────────────────────────────────────────────────────────
const editing = ref(''); // '' = a new preset
const editName = ref('');
const files = ref<Record<string, string>>({ 'main.tf': '' });
const newFile = ref('');
const editBuiltin = ref(false);
const editLocked = ref(false);
const presetSaving = ref(false);
const confirmDelete = ref(false);
const editOptions = computed(() => [{ value: '', label: 'New preset' }, ...presetOptions.value]);
const editLockFile = computed(() => '.terraform.lock.hcl' in files.value);
const readOnly = computed(() => editBuiltin.value || editLocked.value);

watch(editing, async (name) => {
  if (!name) {
    editName.value = '';
    files.value = { 'main.tf': '' };
    editBuiltin.value = editLocked.value = false;
    return;
  }
  const r = await controlApi.preset(name);
  if (editing.value !== name) return;
  if (!r.ok) {
    toast(why(r, `Read ${name}`), 'danger');
    return;
  }
  editName.value = name;
  files.value = { ...(r.body.files ?? {}) };
  editBuiltin.value = r.body.builtin;
  editLocked.value = r.body.locked;
});
function addFile(): void {
  const f = newFile.value.trim();
  if (f && !(f in files.value)) files.value = { ...files.value, [f]: '' };
  newFile.value = '';
}
async function savePreset(): Promise<void> {
  const name = editName.value.trim();
  presetSaving.value = true;
  const r = await controlApi.savePreset(name, files.value);
  presetSaving.value = false;
  if (!r.ok) {
    toast(why(r, `Save ${name}`), 'danger');
    return;
  }
  toast(`Saved ${name}.`, 'success');
  await load();
  editing.value = name;
}
async function deletePreset(): Promise<void> {
  const name = editing.value;
  presetSaving.value = true;
  const r = await controlApi.deletePreset(name);
  presetSaving.value = false;
  confirmDelete.value = false;
  if (!r.ok) {
    toast(why(r, `Delete ${name}`), 'danger');
    return;
  }
  toast(`Deleted ${name}.`, 'success');
  editing.value = '';
  await load();
}
</script>

<template>
  <!-- Hidden when the scaler is off and owns nothing. -->
  <HCard v-if="!loaded || loadError || (view && scalerShown(view))" title="Workers">
    <HAlert v-if="loadError" tone="danger" :title="loadError" compact />
    <HText v-else-if="!view" tone="muted">Loading.</HText>
    <HStack v-else :gap="4" direction="column">
      <HAlert v-if="view.enabled === null" tone="warning" title="Docker did not answer." compact />
      <HDescriptionList :items="facts" variant="inline">
        <template #value:host><code>{{ report?.hostId || '—' }}</code></template>
      </HDescriptionList>

      <HDataTable v-if="workerRows.length" label="Owned workers" :rows="workerRows" :columns="workerColumns" />
      <HText v-else tone="muted">No workers.</HText>

      <HStack v-for="o in orphans" :key="o" direction="row" :gap="2" align="center" wrap>
        <HText>Possible orphan <code>{{ o }}</code></HText>
        <HButton v-if="canAdmin" size="compact" variant="secondary" :loading="saving" @click="dismiss(o)">Dismiss</HButton>
      </HStack>

      <HSettingsPage
        class="settings"
        title="Scaling"
        :description="canAdmin ? 'Per-worker values apply to new workers.' : 'Admin only.'"
        :dirty="dirty"
        :saving="saving"
        :error="saveError || (dirty && !valid ? 'Fix the highlighted fields.' : '')"
        @save="save"
        @reset="reset"
      >
        <HStack :gap="3" direction="column">
          <HSelect
            v-model="draft.preset"
            label="Preset"
            placeholder="Choose a preset"
            :options="presetOptions"
            :disabled="locked || !canAdmin"
            :hint="chosen && !chosen.preset.lockFile ? 'No lock file: providers download unpinned.' : undefined"
          />
          <HText v-if="locked" tone="muted">Locked while workers exist.</HText>
          <HInput
            v-for="v in vars"
            :key="v.name"
            v-model="draft.vars[v.name]"
            :label="v.name"
            :disabled="!canAdmin || (v.shared && locked)"
            :hint="v.shared ? 'Shared.' : 'Per worker.'"
          />
          <HNumberInput v-model="draft.min" label="Min" :min="0" :disabled="!canAdmin" :error="problems.min" />
          <HNumberInput v-model="draft.max" label="Max" :min="0" :disabled="!canAdmin" :error="problems.max" />
          <HNumberInput v-model="draft.cooldownMin" label="Cooldown (min)" :min="0" :disabled="!canAdmin" :error="problems.cooldownMin" />
          <HNumberInput v-model="draft.graceMin" label="Grace (min)" :min="0" :disabled="!canAdmin" :error="problems.graceMin" />
          <template v-if="canAdmin">
            <HInput
              v-for="k in credKeys"
              :key="k"
              v-model="creds[k]"
              type="password"
              :label="k"
              autocomplete="new-password"
              hint="Write-only. Leave empty to keep."
            />
            <HInput v-model="newKey" label="New credential" placeholder="HCLOUD_TOKEN" autocomplete="off" />
            <HInput v-if="newKey.trim()" v-model="newValue" type="password" :label="`${newKey.trim()} value`" autocomplete="new-password" hint="Write-only." />
          </template>
        </HStack>
      </HSettingsPage>

      <HCard v-if="canAdmin" title="Presets" description="Built-ins are read-only.">
        <HStack :gap="3" direction="column">
          <HSelect v-model="editing" label="Edit" :options="editOptions" />
          <HInput v-if="!editing" v-model="editName" label="Name" autocomplete="off" />
          <HText v-if="editLocked" tone="muted">Locked while workers exist.</HText>
          <HText v-if="!editLockFile" tone="muted">No lock file: providers download unpinned.</HText>
          <MonoTextarea v-for="(_, f) in files" :key="f" v-model="files[f]" :label="String(f)" :rows="8" :readonly="readOnly" />
          <template v-if="!readOnly">
            <HStack direction="row" :gap="2" align="end" wrap>
              <HInput v-model="newFile" label="File name" placeholder="network.tf" autocomplete="off" />
              <HButton :disabled="!newFile.trim()" @click="addFile">Add file</HButton>
            </HStack>
            <HStack direction="row" :gap="2" wrap>
              <HButton variant="primary" :loading="presetSaving" :disabled="!editName.trim()" @click="savePreset">Save preset</HButton>
              <HButton v-if="editing" variant="danger" :disabled="presetSaving" @click="confirmDelete = true">Delete</HButton>
            </HStack>
          </template>
        </HStack>
        <HAlertDialog
          v-model:open="confirmDelete"
          danger
          :title="`Delete ${editing}?`"
          confirm-label="Delete"
          :busy="presetSaving"
          @confirm="deletePreset"
        />
      </HCard>
    </HStack>
  </HCard>
</template>

<style scoped>
/* Hearth sizes a settings page title as a page heading; inside a card it is a section. */
.settings :deep(h1) { font-size: 1.125rem; line-height: 1.4; }
</style>
