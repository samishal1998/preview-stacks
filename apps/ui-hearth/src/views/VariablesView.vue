<script setup lang="ts">
/**
 * Host variables and secrets. A variable's value reads back; a secret's goes in and never comes out,
 * so its list shows name and age only. Specs use `${vars.NAME}` / `${secrets.NAME}`.
 */
import { computed, ref } from 'vue';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HCard,
  HDataTable,
  HEmptyState,
  HHeading,
  HInput,
  HPageHeader,
  HSegmentedControl,
  HSheet,
  HStack,
  HSwitch,
  HText,
  HTextarea,
  type MenuAction,
  type TableColumn,
  type TableRow,
} from '@hearth-ui/vue';
import { failure, hostcfg } from '../api-hostcfg';
import { ago } from '../format';
import { WITHHELD, importPlan, secretPairs, validName, varPairs, type Entry } from '../lib/hostcfg';
import { FORMAT_LABEL, VAR_FORMATS, formatVars, parseVars, type VarFormat, type VarPair } from '../lib/varformats';
import { can } from '../lib/users';
import { auth, toast } from '../state';

/** Courtesy: adding, editing, deleting and merging are maintainer's. The server still decides. */
const manage = computed(() => can('maintainer', auth));
const entries = ref<Entry[]>([]);
const loaded = ref(false);
const error = ref('');
const unsupported = ref(false);
const vars = computed(() => entries.value.filter((e) => !e.secret));
const secrets = computed(() => entries.value.filter((e) => e.secret));

async function load(): Promise<void> {
  const r = await hostcfg.list();
  loaded.value = true;
  if (r.status === 404) {
    unsupported.value = true;
    return;
  }
  if (!r.ok) {
    error.value = failure(r);
    return;
  }
  error.value = '';
  entries.value = r.body.entries ?? [];
}
void load();

// ── tables ──────────────────────────────────────────────────────────────────────────────────────
const varCols: TableColumn[] = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'value', label: 'Value', truncate: true },
  { key: 'updated', label: 'Updated', align: 'end', hideBelow: 520 },
];
const secretCols: TableColumn[] = [
  { key: 'name', label: 'Name', sortable: true },
  { key: 'updated', label: 'Updated', align: 'end', hideBelow: 420 },
];
const rowsOf = (es: Entry[], withValue: boolean): TableRow[] =>
  es.map((e) => ({ id: e.name, name: e.name, ...(withValue ? { value: e.value ?? '' } : {}), updated: ago(e.updatedAt) }));
const varRows = computed(() => rowsOf(vars.value, true));
const secretRows = computed(() => rowsOf(secrets.value, false));
const actionsFor = (secret: boolean): MenuAction[] => [
  { id: 'edit', label: secret ? 'Replace' : 'Edit' },
  { id: 'delete', label: 'Delete', danger: true, separatorBefore: true },
];
const varActions = actionsFor(false);
const secretActions = actionsFor(true);

function onAction(e: { id: string; action: string }): void {
  const entry = entries.value.find((x) => x.name === e.id);
  if (!entry) return;
  if (e.action === 'edit') startEdit(entry);
  else if (e.action === 'delete') confirmDel.value = entry;
}

// ── add / edit ──────────────────────────────────────────────────────────────────────────────────
const form = ref({ name: '', value: '', secret: false });
const editing = ref<Entry | null>(null);
const saving = ref(false);
const nameError = computed(() => (form.value.name && !validName(form.value.name) ? 'Letters, digits and _; no leading digit.' : ''));
const canSave = computed(() => !saving.value && validName(form.value.name) && form.value.value.length > 0);

const formOpen = ref(false);
function startAdd(): void {
  cancelEdit();
  formOpen.value = true;
}
function startEdit(e: Entry): void {
  editing.value = e;
  form.value = { name: e.name, value: e.value ?? '', secret: e.secret };
  formOpen.value = true;
}
function cancelEdit(): void {
  editing.value = null;
  form.value = { name: '', value: '', secret: false };
  formOpen.value = false;
}
async function save(): Promise<void> {
  saving.value = true;
  const { name, value, secret } = form.value;
  const r = await hostcfg.put(name, value, secret);
  saving.value = false;
  if (!r.ok) {
    toast(`Could not store ${name}`, 'danger', failure(r));
    return;
  }
  toast(`${editing.value ? 'Saved' : 'Added'} ${name}`, 'success');
  cancelEdit();
  void load();
}

// ── delete ──────────────────────────────────────────────────────────────────────────────────────
const confirmDel = ref<Entry | null>(null);
const deleting = ref(false);
async function remove(): Promise<void> {
  const e = confirmDel.value;
  if (!e) return;
  deleting.value = true;
  const r = await hostcfg.del(e.name);
  deleting.value = false;
  if (!r.ok) {
    toast(`Could not delete ${e.name}`, 'danger', failure(r));
    return;
  }
  toast(`Deleted ${e.name}`, 'success');
  if (editing.value?.name === e.name) cancelEdit();
  confirmDel.value = null;
  void load();
}

// ── copy / paste ────────────────────────────────────────────────────────────────────────────────
const target = ref<'vars' | 'secrets'>('vars');
const fmt = ref<'auto' | VarFormat>('auto');
const text = ref('');
const fallback = ref('');
const importing = ref(false);
const targetOpts = [{ value: 'vars', label: 'Variables' }, { value: 'secrets', label: 'Secrets' }];
const fmtOpts = [{ value: 'auto', label: 'Auto' }, ...VAR_FORMATS.map((f) => ({ value: f, label: FORMAT_LABEL[f] }))];

const parsed = computed(() => (text.value.trim() ? parseVars(text.value, fmt.value === 'auto' ? undefined : fmt.value) : null));
const plan = computed(() => importPlan(parsed.value?.pairs ?? []));

async function copy(f: VarFormat): Promise<void> {
  const isSecret = target.value === 'secrets';
  const out = formatVars(isSecret ? secretPairs(entries.value) : varPairs(entries.value), f, isSecret ? WITHHELD : undefined);
  try {
    await navigator.clipboard.writeText(out);
    fallback.value = '';
    toast(`Copied as ${FORMAT_LABEL[f]}`, 'success');
  } catch {
    fallback.value = out;
    toast('Clipboard unavailable', 'danger', 'Copy the text below.');
  }
}

/** Names the paste would overwrite. Secrets cannot be read back, so an overwrite is final. */
const clashes = computed(() => plan.value.use.filter((p) => entries.value.some((e) => e.name === p.k)).map((p) => p.k));
const confirmMerge = ref(false);
function askMerge(): void {
  if (clashes.value.length) confirmMerge.value = true;
  else void merge();
}

/** One PUT per entry (no bulk route); failures are named and stay in the box to retry. */
async function merge(): Promise<void> {
  confirmMerge.value = false;
  const secret = target.value === 'secrets';
  importing.value = true;
  const failed: VarPair[] = [];
  for (const p of plan.value.use) {
    const r = await hostcfg.put(p.k, p.v, secret);
    if (!r.ok) failed.push(p);
  }
  importing.value = false;
  const stored = plan.value.use.length - failed.length;
  if (failed.length) {
    toast(`Stored ${stored} of ${plan.value.use.length}`, 'danger', `Failed: ${failed.map((p) => p.k).join(', ')}`);
    text.value = formatVars(failed, 'env');
  } else {
    toast(`Stored ${stored}`, 'success');
    text.value = '';
  }
  void load();
}
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Variables and secrets" description="Host values every spec can reference." actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" @click="load" />
        <HButton v-if="!unsupported && manage" variant="primary" icon="plus" label="Add" @click="startAdd" />
      </template>
    </HPageHeader>

    <HEmptyState v-if="unsupported" title="No variable store" description="Upgrade the host." icon="settings" />
    <HAlert v-else-if="error" tone="danger" title="Could not load" :description="error" />

    <template v-else>
      <HCard :title="`Variables (${vars.length})`">
        <HDataTable
          label="Variables"
          :rows="varRows"
          :columns="varCols"
          :loading="!loaded"
          :get-row-actions="manage ? () => varActions : undefined"
          empty-text="No variables."
          @row-action="onAction"
        />
      </HCard>

      <HCard :title="`Secrets (${secrets.length})`">
        <HDataTable
          label="Secrets"
          :rows="secretRows"
          :columns="secretCols"
          :loading="!loaded"
          :get-row-actions="manage ? () => secretActions : undefined"
          empty-text="No secrets."
          @row-action="onAction"
        />
      </HCard>

      <HCard title="Copy and paste">
        <HStack direction="column" :gap="3" class="narrow">
          <HSegmentedControl v-model="target" label="List" :options="targetOpts" />
          <HStack direction="row" :gap="2" wrap align="center">
            <HButton v-for="f in VAR_FORMATS" :key="f" variant="secondary" size="compact" :label="`Copy ${FORMAT_LABEL[f]}`" @click="copy(f)" />
          </HStack>
          <HText v-if="target === 'secrets'" size="sm" tone="muted">Names only. Values cannot be read back.</HText>
          <HTextarea v-if="fallback" :model-value="fallback" label="Exported text" readonly :rows="6" />
          <template v-if="manage">
          <HHeading :level="3" size="sm">Paste</HHeading>
          <HSegmentedControl v-model="fmt" label="Read as" :options="fmtOpts" />
          <HTextarea v-model="text" label="Paste" :rows="6" placeholder="PR=7&#10;REGION=eu-central" />
          <template v-if="parsed">
            <HText size="sm">
              {{ plan.use.length }} read<template v-if="plan.skipped.length">; {{ plan.skipped.length }} without a value skipped ({{ plan.skipped.join(', ') }})</template>.
              <HBadge :label="FORMAT_LABEL[parsed.format]" />
            </HText>
            <HAlert v-if="clashes.length" tone="warning" title="Overwrites" :description="clashes.join(', ')" />
            <HAlert v-if="parsed.problems.length" tone="warning" :title="`${parsed.problems.length} unreadable`">
              <div v-for="p in parsed.problems" :key="p.line">line {{ p.line }}: <code>{{ p.text }}</code> ({{ p.reason }})</div>
            </HAlert>
          </template>
          <div>
            <HButton label="Merge" :loading="importing" :disabled="!plan.use.length || importing" @click="askMerge" />
          </div>
          </template>
        </HStack>
      </HCard>
    </template>

    <HSheet
      :open="formOpen"
      :title="editing ? (editing.secret ? `Replace ${editing.name}` : `Edit ${editing.name}`) : 'Add a variable or secret'"
      @close="cancelEdit"
    >
      <form @submit.prevent="save">
        <HStack direction="column" :gap="3">
          <HInput v-model="form.name" label="Name" placeholder="DB_PASSWORD" autocomplete="off" :disabled="!!editing" :error="nameError" />
          <HInput
            v-model="form.value"
            label="Value"
            :type="form.secret ? 'password' : 'text'"
            :placeholder="editing?.secret ? 'New value' : ''"
            autocomplete="off"
          />
          <HSwitch v-model="form.secret" label="Secret" description="Write-only. Never shown again." :disabled="editing?.secret === true" />
          <HText size="sm" tone="muted">
            Use as <code>${{ '{' }}{{ form.secret ? 'secrets' : 'vars' }}.{{ form.name || 'NAME' }}{{ '}' }}</code>
          </HText>
          <HStack direction="row" :gap="2" wrap>
            <HButton type="submit" :loading="saving" :disabled="!canSave" :label="editing ? 'Save' : 'Add'" />
            <HButton variant="ghost" label="Cancel" @click="cancelEdit" />
          </HStack>
        </HStack>
      </form>
    </HSheet>

    <HAlertDialog
      :open="!!confirmDel"
      :title="`Delete ${confirmDel?.name ?? ''}?`"
      description="Specs using it stop resolving."
      confirm-label="Delete"
      danger
      :busy="deleting"
      @cancel="confirmDel = null"
      @confirm="remove"
    />
    <HAlertDialog
      :open="confirmMerge"
      :title="`Overwrite ${clashes.length} ${clashes.length === 1 ? 'name' : 'names'}?`"
      :description="clashes.join(', ')"
      confirm-label="Overwrite"
      danger
      :busy="importing"
      @cancel="confirmMerge = false"
      @confirm="merge"
    />
  </HStack>
</template>

<style scoped>
.narrow {
  max-width: 560px;
}
</style>
