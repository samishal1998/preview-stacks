<script setup lang="ts">
/** Traefik: the routes it serves (container labels), custom routes, and its watched dynamic-config directory. */
import { computed, onMounted, reactive, ref } from 'vue';
import {
  HAlert, HAlertDialog, HBadge, HButton, HDataTable, HDialog, HHeading, HInput, HPageHeader, HSelect, HSheet, HStack, HText, HTextarea,
  type MenuAction, type TableColumn, type TableRow,
} from '@hearth-ui/vue';
import { routingApi, type RoutingFile, type RuntimeRoute } from '../api-routing';
import CustomRouteDialogs from '../components/CustomRouteDialogs.vue';
import { hostItems } from '../lib/customRoutes';
import { fileNameError, isPattern, STARTER, targetOf } from '../lib/routing';
import { useCustomRoutes } from '../lib/use-custom-routes';
import { ago } from '../format';
import { can } from '../lib/users';
import { auth, data, toast } from '../state';

const live = ref<RuntimeRoute[]>([]);
const reachable = ref<boolean | null>(null);
const liveError = ref('');
const files = ref<RoutingFile[]>([]);
const dir = ref('');
const writable = ref(false);
/** Courtesy: writing config files is maintainer's. The server still decides. */
const mayEdit = computed(() => writable.value && can('maintainer', auth));
const loaded = ref(false);
const listError = ref('');

const msg = (r: { status: number; body: { error?: string } }, what: string) =>
  r.status === 403 ? 'Maintainer only.' : r.status === 401 ? 'Sign in again.' : `${what}: ${r.body.error ?? `HTTP ${r.status}`}`;

async function loadList(): Promise<void> {
  const r = await routingApi.list();
  loaded.value = true;
  if (!r.ok) {
    listError.value = msg(r, 'Could not load files');
    return;
  }
  listError.value = '';
  files.value = r.body.files ?? [];
  dir.value = r.body.dir ?? '';
  writable.value = !!r.body.writable;
}
async function loadLive(): Promise<void> {
  const r = await routingApi.live();
  if (!r.ok) {
    liveError.value = msg(r, 'Could not load routes');
    return;
  }
  liveError.value = '';
  reachable.value = r.body.reachable;
  live.value = r.body.routes ?? [];
}
const refreshing = ref(false);
async function refreshAll(): Promise<void> {
  refreshing.value = true;
  await Promise.all([loadList(), loadLive(), cr.load()]);
  refreshing.value = false;
}
onMounted(refreshAll);

// ── live routes ─────────────────────────────────────────────────────────────────────────────────
const liveColumns: TableColumn[] = [
  { key: 'url', label: 'URL' },
  { key: 'target', label: 'Forwards to', hideBelow: 560 },
  { key: 'deployment', label: 'Deployment', hideBelow: 720 },
  { key: 'router', label: 'Router', truncate: true, hideBelow: 900 },
];
const liveRows = computed<TableRow[]>(() =>
  live.value.map((r) => ({ id: `${r.container}/${r.router}`, url: r.hosts.join(' '), target: '', deployment: r.project ?? '', router: r.router })),
);
const routeOf = (row: TableRow) => live.value.find((r) => `${r.container}/${r.router}` === row.id);

// ── custom routes ───────────────────────────────────────────────────────────────────────────────
const cr = useCustomRoutes();
const crColumns: TableColumn[] = [
  { key: 'name', label: 'Route' },
  { key: 'hosts', label: 'Hosts' },
  { key: 'deployment', label: 'Assigned to', minWidth: '12rem' },
];
const crRows = computed<TableRow[]>(() => cr.routes.value.map((r) => ({ id: r.name, name: r.name, hosts: '', deployment: r.deployment ?? '' })));
const crOf = (row: TableRow) => cr.routes.value.find((r) => r.name === row.id);
const deploymentOptions = computed(() => [
  { value: '', label: 'Unassigned' },
  ...data.deployments.map((d) => ({ value: d.id, label: d.stack ?? d.id })),
]);
/** The cell's select: a deployment assigns (or asks to move), Unassigned asks to unassign. */
// A native select keeps what was picked even when the model does not change (a cancelled move, a
// refused assign), so each pick remounts the selects: they show the holder until the list moves.
const picks = ref(0);
function pick(name: string, deployment: string): void {
  picks.value++;
  if (deployment) void cr.assign(name, deployment);
  else cr.drop.value = { name, kind: 'unassign' };
}
const crActions = (row: TableRow): MenuAction[] => [
  { id: 'unassign', label: 'Unassign', disabled: !crOf(row)?.deployment },
  { id: 'delete', label: 'Delete', danger: true, separatorBefore: true },
];
function onCrAction(e: { id: string; action: string }): void {
  cr.drop.value = { name: e.id, kind: e.action === 'delete' ? 'delete' : 'unassign' };
}
const draftRoute = reactive({ open: false, name: '', deployment: '', error: '' });
async function createRoute(): Promise<void> {
  draftRoute.error = await cr.create(draftRoute.name.trim(), draftRoute.deployment);
  if (!draftRoute.error) Object.assign(draftRoute, { open: false, name: '', deployment: '' });
}

// ── files ───────────────────────────────────────────────────────────────────────────────────────
const fileColumns: TableColumn[] = [
  { key: 'name', label: 'File' },
  { key: 'size', label: 'Size', align: 'end', hideBelow: 420 },
  { key: 'updated', label: 'Updated', align: 'end', hideBelow: 560 },
];
const fileRows = computed<TableRow[]>(() => files.value.map((f) => ({ id: f.name, name: f.name, size: `${f.size} B`, updated: ago(f.updatedAt) })));

/** `null`: closed. `''`: a new file. */
const openName = ref<string | null>(null);
const newName = ref('');
const draft = ref('');
const original = ref('');
const withheld = ref(false);
const loadingFile = ref(false);
const saving = ref(false);
const fileError = ref('');
const undo = ref<{ name: string; content: string } | null>(null);
const askDelete = ref(false);

const isNew = computed(() => openName.value === '');
const dirty = computed(() => draft.value !== original.value);
const nameError = computed(() => (isNew.value && newName.value ? fileNameError(newName.value) : ''));
const canSave = computed(() => mayEdit.value && !loadFailed.value && !loadingFile.value && !saving.value && !withheld.value && draft.value.trim() !== '' && dirty.value && (!isNew.value || fileNameError(newName.value) === ''));

const loadFailed = ref(false);
const deleting = ref(false);
const askClose = ref(false);

async function open(name: string): Promise<void> {
  openName.value = name;
  draft.value = original.value = '';
  loadingFile.value = true;
  loadFailed.value = false;
  fileError.value = '';
  withheld.value = false;
  const r = await routingApi.read(name);
  if (openName.value !== name) return; // closed or another file opened meanwhile
  loadingFile.value = false;
  if (!r.ok) {
    loadFailed.value = true;
    fileError.value = msg(r, 'Could not open the file');
    return;
  }
  withheld.value = !!r.body.sourceWithheld;
  draft.value = original.value = r.body.content ?? '';
}
function startNew(): void {
  openName.value = '';
  newName.value = '';
  draft.value = STARTER;
  original.value = '';
  withheld.value = false;
  loadFailed.value = false;
  loadingFile.value = false;
  fileError.value = '';
}
function close(): void {
  openName.value = null;
  draft.value = original.value = '';
  loadFailed.value = false;
  loadingFile.value = false;
  fileError.value = '';
  askClose.value = false;
}
/** Sheet close: unsaved edits are confirmed first. */
function tryClose(): void {
  if (dirty.value && !saving.value) askClose.value = true;
  else close();
}

async function save(): Promise<void> {
  const name = isNew.value ? newName.value.trim() : openName.value;
  if (!name) return;
  saving.value = true;
  fileError.value = '';
  const r = await routingApi.write(name, draft.value);
  saving.value = false;
  if (!r.ok) {
    fileError.value = msg(r, 'Not saved'); // the server's text names the YAML or section problem
    return;
  }
  if (r.body.previous) undo.value = { name, content: r.body.previous };
  original.value = draft.value;
  openName.value = name;
  toast(`Saved ${name}.`, 'success');
  void loadList();
}
async function putBack(): Promise<void> {
  const u = undo.value;
  if (!u) return;
  const r = await routingApi.write(u.name, u.content);
  if (!r.ok) {
    toast(msg(r, 'Not restored'), 'danger');
    return;
  }
  undo.value = null;
  toast(`Restored ${u.name}.`, 'success');
  if (openName.value === u.name) void open(u.name);
  void loadList();
}
async function remove(): Promise<void> {
  const name = openName.value;
  if (!name || deleting.value) return;
  askDelete.value = false;
  deleting.value = true;
  const r = await routingApi.remove(name);
  deleting.value = false;
  if (!r.ok) {
    fileError.value = msg(r, 'Not deleted');
    return;
  }
  if (r.body.previous) undo.value = { name, content: r.body.previous };
  toast(`Deleted ${name}.`, 'success');
  close();
  void loadList();
}
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Routing" description="Routes Traefik serves, and its config files." actions-placement="end">
      <template #actions>
        <HButton v-if="mayEdit" variant="primary" icon="plus" label="New file" @click="startNew" />
        <HButton variant="secondary" icon="arrow" label="Refresh" :loading="refreshing" @click="refreshAll" />
      </template>
    </HPageHeader>

    <HAlert v-if="undo" tone="info" :title="`Replaced ${undo.name}.`" dismissible @dismiss="undo = null">
      <HButton size="compact" variant="secondary" label="Put it back" @click="putBack" />
    </HAlert>

    <HHeading :level="2" size="md">Live routes</HHeading>
    <HAlert v-if="liveError" tone="danger" :description="liveError" />
    <HAlert v-else-if="reachable === false" tone="warning" title="Docker did not answer." description="Routes are unknown, not none." />
    <HDataTable v-else label="Live routes" :rows="liveRows" :columns="liveColumns" :loading="reachable === null" :page-size="10" empty-text="No routes. Deploy a stack.">
      <template #cell="{ row, column, value }">
        <template v-if="column.key === 'url'">
          <HStack direction="column" :gap="1">
            <template v-for="h in routeOf(row)?.hosts ?? []" :key="h">
              <HText v-if="isPattern(h)" tone="muted">{{ h }}</HText>
              <a v-else :href="`https://${h}`" target="_blank" rel="noreferrer">{{ h }}</a>
            </template>
            <HText v-if="!(routeOf(row)?.hosts.length)" tone="muted">no host in the rule</HText>
          </HStack>
        </template>
        <template v-else-if="column.key === 'target' && routeOf(row)">
          <HBadge v-if="targetOf(routeOf(row)!).tone !== 'neutral' || !routeOf(row)!.target" :tone="targetOf(routeOf(row)!).tone" :label="targetOf(routeOf(row)!).text" />
          <code v-else>{{ routeOf(row)!.target }}</code>
        </template>
        <template v-else-if="column.key === 'deployment'">
          <RouterLink v-if="value" :to="`/deployments/${encodeURIComponent(String(value))}/runtime`">{{ value }}</RouterLink>
          <template v-else>—</template>
        </template>
        <template v-else>{{ value }}</template>
      </template>
    </HDataTable>

    <HStack direction="row" :gap="3" wrap align="center" justify="between">
      <HHeading :level="2" size="md">Custom routes</HHeading>
      <HButton v-if="cr.mayWrite.value" variant="secondary" icon="plus" label="New route" @click="draftRoute.open = true" />
    </HStack>
    <HAlert v-if="cr.error.value" tone="danger" :description="cr.error.value" />
    <HDataTable
      v-else
      label="Custom routes"
      :rows="crRows"
      :columns="crColumns"
      :loading="!cr.loaded.value"
      :page-size="10"
      :get-row-actions="cr.mayWrite.value ? crActions : undefined"
      empty-text="No custom routes."
      @row-action="onCrAction"
    >
      <template #cell="{ row, column, value }">
        <HStack v-if="column.key === 'hosts'" direction="column" :gap="1">
          <template v-for="h in hostItems(crOf(row)?.hosts ?? [])" :key="h.host">
            <a v-if="h.href" :href="h.href" target="_blank" rel="noreferrer">{{ h.host }}</a>
            <HText v-else tone="muted">{{ h.host }}</HText>
          </template>
          <template v-if="!crOf(row)?.hosts.length">—</template>
        </HStack>
        <HSelect
          v-else-if="column.key === 'deployment' && cr.mayWrite.value"
          :key="picks"
          :label="`Deployment for ${row.name}`"
          hide-label
          size="compact"
          :model-value="String(value)"
          :options="deploymentOptions"
          :disabled="cr.busy.value"
          @update:model-value="(v: string) => pick(String(row.id), v)"
        />
        <template v-else-if="column.key === 'deployment'">
          <RouterLink v-if="value" :to="`/deployments/${encodeURIComponent(String(value))}`">{{ crOf(row)?.stack ?? value }}</RouterLink>
          <HText v-else tone="muted">Unassigned</HText>
        </template>
        <template v-else>{{ value }}</template>
      </template>
    </HDataTable>

    <HHeading :level="2" size="md">Config files</HHeading>
    <HText v-if="dir" tone="muted">{{ dir }}</HText>
    <HAlert v-if="listError" tone="danger" :description="listError" />
    <HAlert
      v-else-if="loaded && !mayEdit"
      tone="warning"
      title="Read-only."
      :description="writable ? 'Editing needs the maintainer role.' : 'The API cannot write here. Re-run setup on the host.'"
    />
    <HDataTable
      v-if="!listError"
      label="Config files"
      :rows="fileRows"
      :columns="fileColumns"
      :loading="!loaded"
      :page-size="10"
      row-activatable
      empty-text="No files. Everything is routed by labels."
      @row-activate="open(String($event.id))"
    />

    <HSheet :open="openName !== null" :title="isNew ? 'New file' : (openName ?? '')" width="640px" @close="tryClose">
      <HStack direction="column" :gap="3">
        <HInput v-if="isNew" v-model="newName" label="File name" placeholder="middleware.yml" :error="nameError" autocomplete="off" />
        <HAlert v-if="loadingFile" tone="info" title="Loading." compact />
        <template v-else-if="loadFailed" />
        <HAlert v-else-if="withheld" tone="warning" title="Hidden." description="Sign in to view this file." />
        <HTextarea v-else v-model="draft" label="Contents" :rows="18" :readonly="!mayEdit" spellcheck="false" />
        <HAlert v-if="fileError" tone="danger" :description="fileError" />
      </HStack>
      <template #footer>
        <HStack direction="row" :gap="2" wrap>
          <HButton variant="primary" label="Save" :loading="saving" :disabled="!canSave" @click="save" />
          <HBadge v-if="dirty && !withheld" tone="warning" label="Unsaved" />
          <HButton v-if="openName && mayEdit" variant="danger" label="Delete" :disabled="deleting" @click="askDelete = true" />
          <HButton variant="ghost" label="Close" @click="tryClose" />
        </HStack>
      </template>
    </HSheet>

    <HAlertDialog
      v-model:open="askDelete"
      danger
      :title="`Delete ${openName}?`"
      description="Its routes and middleware stop applying."
      confirm-label="Delete"
      :busy="deleting"
      @confirm="remove"
    />
    <HDialog :open="draftRoute.open" title="New route" @close="draftRoute.open = false">
      <form @submit.prevent="createRoute">
        <HStack direction="column" :gap="3">
          <HInput v-model="draftRoute.name" label="Name" placeholder="checkout" autocomplete="off" />
          <HSelect v-model="draftRoute.deployment" label="Assign to" :options="deploymentOptions" />
          <HAlert v-if="draftRoute.error" tone="danger" :description="draftRoute.error" />
        </HStack>
      </form>
      <template #footer>
        <HButton variant="ghost" label="Cancel" @click="draftRoute.open = false" />
        <HButton variant="primary" label="Create" :loading="cr.busy.value" :disabled="!draftRoute.name.trim()" @click="createRoute" />
      </template>
    </HDialog>
    <CustomRouteDialogs :routes="cr" />
    <HAlertDialog
      v-model:open="askClose"
      title="Discard changes?"
      description="Unsaved edits are lost."
      confirm-label="Discard"
      @confirm="close"
    />
  </HStack>
</template>
