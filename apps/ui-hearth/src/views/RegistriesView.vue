<script setup lang="ts">
/** Private registry credentials. Write-only: the list shows hostnames and usernames, never a secret. */
import { computed, ref } from 'vue';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HCard,
  HDataTable,
  HEmptyState,
  HInput,
  HPageHeader,
  HStack,
  HText,
  type MenuAction,
  type TableColumn,
  type TableRow,
} from '@hearth-ui/vue';
import { registriesApi, type RegistryEntry } from '../api-registries';
import { problem } from '../lib/settings-problem';
import { can } from '../lib/users';
import { auth, toast } from '../state';

const entries = ref<RegistryEntry[]>([]);
const helpers = ref<string[]>([]);
const dir = ref('');
/** Courtesy: adding and forgetting are maintainer's. The server still decides. */
const manage = computed(() => can('maintainer', auth));
const writable = ref<boolean | null>(null);
const loaded = ref(false);
const loadError = ref('');
const forbidden = ref(false);

const host = ref('');
const username = ref('');
const password = ref('');
const saving = ref(false);
const formError = ref('');
const canSave = computed(() => !saving.value && !!host.value.trim() && !!username.value.trim() && !!password.value);

const target = ref<string | null>(null);
const forgetting = ref(false);

async function load(): Promise<void> {
  const r = await registriesApi.list();
  loaded.value = true;
  forbidden.value = r.status === 403;
  if (!r.ok) {
    loadError.value = forbidden.value ? '' : problem(r, 'Load failed');
    return;
  }
  loadError.value = '';
  entries.value = r.body.entries ?? [];
  helpers.value = r.body.helpers ?? [];
  dir.value = r.body.dir ?? '';
  writable.value = r.body.writable ?? false;
}
void load();

async function save(): Promise<void> {
  saving.value = true;
  formError.value = '';
  const r = await registriesApi.put(host.value.trim(), username.value.trim(), password.value);
  saving.value = false;
  if (!r.ok) {
    formError.value = problem(r, 'Could not store this credential');
    return;
  }
  password.value = '';
  const name = r.body.registry || host.value.trim();
  toast(`${entries.value.some((e) => e.registry === name) ? 'Saved' : 'Added'} ${name}`, 'success');
  host.value = '';
  username.value = '';
  void load();
}

async function forget(): Promise<void> {
  const name = target.value;
  if (!name) return;
  forgetting.value = true;
  const r = await registriesApi.forget(name);
  forgetting.value = false;
  target.value = null;
  if (!r.ok) {
    toast(problem(r, 'Could not forget this credential'), 'danger');
    return;
  }
  toast(`Forgot ${name}`, 'success');
  void load();
}

const columns: TableColumn[] = [
  { key: 'registry', label: 'Registry', truncate: true },
  { key: 'username', label: 'Username', hideBelow: 420 },
  { key: 'via', label: 'Source', hideBelow: 520 },
];
const rows = computed<TableRow[]>(() =>
  entries.value.map((e) => ({ id: e.registry, registry: e.registry, username: e.username ?? 'unknown', via: e.viaHelper ? 'helper' : 'file' })),
);
const rowActions = (): MenuAction[] => [{ id: 'forget', label: 'Forget', danger: true, icon: 'close' }];
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Registries" description="Credentials for pulling private images." actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" @click="load" />
      </template>
    </HPageHeader>

    <HEmptyState v-if="forbidden" icon="lock" title="Maintainer only" description="Your account cannot manage registries." />

    <template v-else>
      <HAlert v-if="loadError" tone="danger" title="Could not load registries" :description="loadError" />
      <HAlert v-if="writable === false" tone="warning" title="Credentials cannot be saved here" description="Re-run setup on the host." />
      <HAlert
        v-if="helpers.length"
        tone="warning"
        title="Credential helper in use"
        :description="`${helpers.join(', ')}: private pulls fail here. Add the credential below.`"
      />

      <HCard v-if="!loadError" title="Stored" :description="dir || undefined">
        <HDataTable
          label="Stored registry credentials"
          :rows="rows"
          :columns="columns"
          :loading="!loaded"
          :get-row-actions="manage ? rowActions : undefined"
          empty-text="No credentials yet."
          @row-action="(e) => (target = e.id)"
        >
          <template #cell="{ column, value }">
            <HBadge v-if="column.key === 'via' && value === 'helper'" label="Via helper" tone="warning" />
            <template v-else>{{ value }}</template>
          </template>
        </HDataTable>
      </HCard>

      <HCard v-if="!loadError && manage" title="Add a credential" description="Applies to the next pull.">
        <form @submit.prevent="save">
          <HStack direction="column" :gap="3" style="max-width: 24rem">
            <HInput v-model="host" label="Registry" placeholder="ghcr.io" autocomplete="off" required />
            <HInput v-model="username" label="Username" autocomplete="off" required />
            <HInput
              v-model="password"
              type="password"
              label="Password or token"
              autocomplete="off"
              hint="Stored write-only. Never shown again."
              required
            />
            <HAlert v-if="formError" tone="danger" :description="formError" />
            <HStack direction="row" :gap="3" align="center">
              <HButton type="submit" variant="primary" label="Add credential" :loading="saving" :disabled="!canSave || writable === false" />
              <HText v-if="writable === false" tone="muted" size="sm">Not writable.</HText>
            </HStack>
          </HStack>
        </form>
      </HCard>
    </template>

    <HAlertDialog
      :open="target !== null"
      danger
      :title="`Forget ${target}?`"
      description="Pulls from this registry fail until it is added again."
      confirm-label="Forget"
      :busy="forgetting"
      @update:open="(o: boolean) => { if (!o) target = null; }"
      @confirm="forget"
    />
  </HStack>
</template>
