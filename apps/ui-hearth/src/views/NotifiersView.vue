<script setup lang="ts">
/**
 * Notifiers: where events on this host get sent. The type picker, fields and event list come from
 * /api/notifiers/meta, so a new server-side type needs no change here. A signing secret is shown once.
 */
import { computed, ref, watch } from 'vue';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HCard,
  HCheckbox,
  HCopyField,
  HDataTable,
  HDialog,
  HEmptyState,
  HInput,
  HMultiSelect,
  HPageHeader,
  HSelect,
  HSheet,
  HStack,
  HText,
  type MenuAction,
  type TableColumn,
  type TableRow,
} from '@hearth-ui/vue';
import type { DeliveryRow, NotifierRow } from '@samyx/preview-stacks-client';
import { notifiersApi, type NotifierMeta } from '../api-notifiers';
import { ago, stamp } from '../format';
import { canCreate, configFor, eventsFor, targetOf } from '../lib/notifiers';
import { byLabel } from '../lib/labels';
import { can, sentence } from '../lib/users';
import { problem } from '../lib/settings-problem';
import { auth, toast } from '../state';

const notifiers = ref<NotifierRow[]>([]);
const meta = ref<NotifierMeta | null>(null);
const loaded = ref(false);
const error = ref('');
/** Courtesy: registering, testing, toggling and removing are maintainer's. The server still decides. */
const manage = computed(() => can('maintainer', auth));
const unsupported = ref(false);
const forbidden = ref(false);

const revealed = ref<{ name: string; secret: string } | null>(null);

const form = ref({ name: '', type: 'webhook', all: false, picked: [] as string[], config: {} as Record<string, string> });
const saving = ref(false);
const formError = ref('');

const chosen = computed(() => meta.value?.types.find((t) => t.kind === form.value.type) ?? null);
const typeOptions = computed(() => (meta.value?.types ?? []).map((t) => ({ value: t.kind, label: t.label })));
const eventOptions = computed(() => (meta.value?.events ?? []).map((e) => ({ value: e, label: e })));
const events = computed(() => eventsFor(form.value.all, form.value.picked, meta.value?.wildcard ?? '*'));
const valid = computed(() => canCreate({ name: form.value.name, events: events.value, config: form.value.config }, chosen.value));

// One config bag is shared by every type; switching drops the old type's fields.
watch(() => form.value.type, () => (form.value.config = {}));

async function load(): Promise<void> {
  const [list, m] = await Promise.all([notifiersApi.list(), notifiersApi.meta()]);
  loaded.value = true;
  forbidden.value = list.status === 403;
  unsupported.value = list.status === 404 || m.status === 404;
  if (unsupported.value || forbidden.value) return;
  if (!list.ok) {
    error.value = problem(list, 'Load failed');
    return;
  }
  error.value = '';
  notifiers.value = list.body.notifiers ?? [];
  if (m.ok) {
    meta.value = m.body;
    if (!m.body.types.some((t) => t.kind === form.value.type) && m.body.types[0]) form.value.type = m.body.types[0].kind;
  }
}
void load();

async function create(): Promise<void> {
  saving.value = true;
  formError.value = '';
  const r = await notifiersApi.create({
    name: form.value.name.trim(),
    type: form.value.type,
    events: events.value,
    config: configFor(chosen.value, form.value.config),
  });
  saving.value = false;
  if (!r.ok) {
    formError.value = problem(r, 'Could not register this notifier');
    return;
  }
  // null for a type whose config already carries its credential.
  revealed.value = r.body.secret ? { name: r.body.notifier.name, secret: r.body.secret } : null;
  form.value = { name: '', type: form.value.type, all: false, picked: [], config: {} };
  toast('Notifier added', 'success', revealed.value ? 'Copy the signing secret.' : undefined);
  void load();
}

const named = computed(() => byLabel(notifiers.value, (n) => n.name, (n) => String(n.id)));
const byId = (id: string) => named.value.get(id);

async function setEnabled(n: NotifierRow): Promise<void> {
  const r = await notifiersApi.setEnabled(n.id, !n.enabled);
  if (!r.ok) toast(problem(r, 'Could not update this notifier'), 'danger');
  void load();
}

const testing = ref<number | null>(null);
async function test(n: NotifierRow): Promise<void> {
  if (testing.value !== null) return;
  testing.value = n.id;
  const r = await notifiersApi.test(n.id);
  testing.value = null;
  if (!r.ok) {
    toast(problem(r, 'Could not send a test'), 'danger');
    return;
  }
  const res = r.body.result;
  if (res.ok) toast('Test delivered', 'success', n.name);
  else toast(`${n.name}: ${res.error ?? `HTTP ${res.status}`}`, 'danger');
  void load();
}

// ── remove ──────────────────────────────────────────────────────────────────────────────────────
const removing = ref<NotifierRow | null>(null);
const removeBusy = ref(false);
async function remove(): Promise<void> {
  const n = removing.value;
  if (!n) return;
  removeBusy.value = true;
  const r = await notifiersApi.remove(n.id);
  removeBusy.value = false;
  removing.value = null;
  if (!r.ok) {
    toast(problem(r, 'Could not remove this notifier'), 'danger');
    return;
  }
  if (open.value?.id === n.id) open.value = null;
  toast(`Removed ${n.name}`, 'success');
  void load();
}

// ── deliveries ──────────────────────────────────────────────────────────────────────────────────
const open = ref<NotifierRow | null>(null);
const deliveries = ref<DeliveryRow[]>([]);
const queued = ref(0);
const dError = ref('');
const dLoading = ref(false);
const redeliver = ref<DeliveryRow | null>(null);
const redeliverBusy = ref(false);

async function loadDeliveries(): Promise<void> {
  const target = open.value;
  if (!target) return;
  dLoading.value = true;
  const r = await notifiersApi.deliveries(target.id);
  // Another notifier (or none) is open now: this answer belongs to a sheet that is gone.
  if (open.value?.id !== target.id) return;
  dLoading.value = false;
  if (!r.ok) {
    dError.value = problem(r, 'Could not load the delivery log');
    return;
  }
  dError.value = '';
  deliveries.value = r.body.deliveries ?? [];
  queued.value = r.body.queued ?? 0;
}
function showDeliveries(n: NotifierRow): void {
  open.value = n;
  deliveries.value = [];
  queued.value = 0;
  void loadDeliveries();
}

// Replays the stored envelope with its original id, so a receiver can dedupe.
async function doRedeliver(): Promise<void> {
  const d = redeliver.value;
  if (!d || !open.value) return;
  redeliverBusy.value = true;
  const r = await notifiersApi.redeliver(open.value.id, d.id);
  redeliverBusy.value = false;
  redeliver.value = null;
  if (!r.ok) {
    toast(problem(r, 'Could not redeliver this event'), 'danger');
    return;
  }
  toast(r.body.note ?? 'Queued for redelivery', 'success');
  await loadDeliveries();
}

// ── tables ──────────────────────────────────────────────────────────────────────────────────────
const columns: TableColumn[] = [
  { key: 'name', label: 'Name', truncate: true },
  { key: 'events', label: 'Events', hideBelow: 520, truncate: true },
  { key: 'last', label: 'Last delivery', hideBelow: 420 },
  { key: 'state', label: 'State' },
];
const rows = computed<TableRow[]>(() =>
  [...named.value].map(([rid, n]) => ({
    id: rid,
    name: n.name,
    sub: `${sentence(n.type)} · ${targetOf(n.config)}`,
    events: n.events.includes('*') ? 'All events' : n.events.join(', '),
    status: n.lastStatus ?? '',
    last: n.lastAt ? ago(n.lastAt) : 'never',
    state: n.enabled ? 'enabled' : 'disabled',
  })),
);
const rowActions = (row: TableRow): MenuAction[] => {
  const n = byId(row.id);
  if (!manage.value) return [{ id: 'deliveries', label: 'Deliveries', icon: 'clock' }];
  return [
    { id: 'deliveries', label: 'Deliveries', icon: 'clock' },
    { id: 'test', label: 'Send test', icon: 'launch', disabled: testing.value !== null },
    { id: 'toggle', label: n?.enabled ? 'Disable' : 'Enable' },
    { id: 'remove', label: 'Remove', danger: true, separatorBefore: true, icon: 'close' },
  ];
};
function onRowAction(e: { id: string; action: string }): void {
  const n = byId(e.id);
  if (!n) return;
  if (e.action === 'deliveries') showDeliveries(n);
  else if (e.action === 'test') void test(n);
  else if (e.action === 'toggle') void setEnabled(n);
  else removing.value = n;
}

const dColumns: TableColumn[] = [
  { key: 'status', label: 'Status' },
  { key: 'event', label: 'Event', truncate: true },
  { key: 'when', label: 'When', hideBelow: 420 },
  { key: 'detail', label: 'Detail', hideBelow: 520, truncate: true },
];
const dNamed = computed(() => byLabel(deliveries.value, (d) => `${d.event}, ${stamp(d.createdAt)}`, (d) => String(d.id)));
const dRows = computed<TableRow[]>(() =>
  [...dNamed.value].map(([rid, d]) => ({
    id: rid,
    status: d.status,
    event: d.event,
    when: stamp(d.createdAt),
    detail: [`${d.attempts} attempt${d.attempts === 1 ? '' : 's'}`, d.responseCode ? `HTTP ${d.responseCode}` : '', d.error ?? ''].filter(Boolean).join(' · '),
  })),
);
const dActions = (row: TableRow): MenuAction[] =>
  manage.value && dNamed.value.get(row.id)?.replayable ? [{ id: 'redeliver', label: 'Redeliver', icon: 'arrow' }] : [];
const statusTone = (s: string) => (s === 'ok' ? 'success' : s === 'failed' ? 'danger' : 'warning');
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Notifiers" description="Where events on this host get sent." actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" @click="load" />
      </template>
    </HPageHeader>

    <HEmptyState v-if="unsupported" icon="info" title="No notifiers" description="This server predates notifiers. Upgrade the host." />
    <HEmptyState v-else-if="forbidden" icon="lock" title="Not allowed" description="Your account cannot read notifiers." />

    <template v-else>
      <HAlert v-if="error" tone="danger" title="Could not load notifiers" :description="error" />

      <HCard v-if="!error" title="Registered">
        <HDataTable
          label="Notifiers"
          :rows="rows"
          :columns="columns"
          :loading="!loaded"
          :get-row-actions="rowActions"
          :empty-text="manage ? 'No notifiers yet. Add one below.' : 'No notifiers yet.'"
          @row-action="onRowAction"
        >
          <template #cell="{ row, column, value }">
            <template v-if="column.key === 'name'">
              <HText weight="medium">{{ value }}</HText>
              <HText as="div" size="sm" tone="muted">{{ row.sub }}</HText>
            </template>
            <HBadge v-else-if="column.key === 'state'" :label="String(value)" :tone="value === 'enabled' ? 'success' : 'neutral'" />
            <HStack v-else-if="column.key === 'last'" direction="row" :gap="2" align="center" wrap>
              <HBadge v-if="row.status" :label="String(row.status)" :tone="statusTone(String(row.status))" />
              <HText size="sm" tone="muted">{{ value }}</HText>
            </HStack>
            <template v-else>{{ value }}</template>
          </template>
        </HDataTable>
      </HCard>

      <HCard v-if="!error && manage" title="Add a notifier">
        <form @submit.prevent="create">
          <HStack direction="column" :gap="3" style="max-width: 28rem">
            <HSelect v-if="(meta?.types.length ?? 0) > 1" v-model="form.type" label="Type" :options="typeOptions" />
            <HInput v-model="form.name" label="Name" placeholder="ops-slack-bridge" autocomplete="off" required />
            <HInput
              v-for="f in chosen?.fields ?? []"
              :key="`${form.type}-${f.key}`"
              :model-value="form.config[f.key] ?? ''"
              :type="f.secret ? 'password' : 'text'"
              :label="f.label"
              :placeholder="f.placeholder"
              :required="f.required"
              autocomplete="off"
              @update:model-value="(v: string) => (form.config[f.key] = v)"
            />
            <HCheckbox v-model="form.all" label="All events" />
            <HMultiSelect
              v-if="!form.all"
              v-model="form.picked"
              label="Events"
              :options="eventOptions"
              placeholder="Choose events"
            />
            <HAlert v-if="formError" tone="danger" :description="formError" />
            <HStack direction="row" :gap="3" align="center" wrap>
              <HButton type="submit" variant="primary" label="Add notifier" :loading="saving" :disabled="!valid || saving" />
              <HText tone="muted" size="sm">
                {{ chosen?.signs !== false ? 'Signed with a secret shown once.' : 'The webhook URL is the credential. Stored write-only.' }}
              </HText>
            </HStack>
          </HStack>
        </form>
      </HCard>
    </template>

    <HDialog :open="!!revealed" :title="`Signing secret for ${revealed?.name ?? ''}`" description="Shown once. Copy it now." @close="revealed = null">
      <HCopyField v-if="revealed" label="Signing secret" :value="revealed.secret" secret />
      <template #footer>
        <HButton variant="primary" label="Done" @click="revealed = null" />
      </template>
    </HDialog>

    <HSheet :open="open !== null" :title="open ? `Deliveries: ${open.name}` : 'Deliveries'" width="42rem" @close="open = null">
      <HStack direction="column" :gap="3">
        <HStack direction="row" :gap="2" align="center" justify="between" wrap>
          <HBadge v-if="queued > 0" :label="`${queued} queued`" tone="info" dot />
          <span v-else />
          <HButton variant="secondary" size="compact" icon="arrow" label="Refresh" :loading="dLoading" @click="loadDeliveries" />
        </HStack>
        <HAlert v-if="dError" tone="danger" :description="dError" />
        <HDataTable
          label="Deliveries"
          :rows="dRows"
          :columns="dColumns"
          :loading="dLoading && !deliveries.length"
          :get-row-actions="dActions"
          empty-text="Nothing delivered yet."
          :page-size="10"
          @row-action="(e) => (redeliver = dNamed.get(e.id) ?? null)"
        >
          <template #cell="{ column, value }">
            <HBadge v-if="column.key === 'status'" :label="String(value)" :tone="statusTone(String(value))" />
            <template v-else>{{ value }}</template>
          </template>
        </HDataTable>
      </HStack>
    </HSheet>

    <HAlertDialog
      :open="removing !== null"
      danger
      :title="`Remove ${removing?.name}?`"
      description="Events stop going there."
      confirm-label="Remove"
      :busy="removeBusy"
      @update:open="(o: boolean) => { if (!o) removing = null; }"
      @confirm="remove"
    />
    <HAlertDialog
      :open="redeliver !== null"
      :title="`Send ${redeliver?.event} again?`"
      description="Sent with its original id, so a receiver can dedupe."
      confirm-label="Redeliver"
      :busy="redeliverBusy"
      @update:open="(o: boolean) => { if (!o) redeliver = null; }"
      @confirm="doRedeliver"
    />
  </HStack>
</template>
