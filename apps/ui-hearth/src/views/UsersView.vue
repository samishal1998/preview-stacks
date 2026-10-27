<script setup lang="ts">
/**
 * Accounts and personal API tokens.
 *
 * Accounts are shared state and any signed-in account reads them; changing them is admin's, so those
 * controls are not drawn for anyone else. Your own password is yours at every role. Tokens are yours
 * alone (the API cannot show anyone else's). Hiding is courtesy: a refused call lands in the alert.
 * Secrets are shown once. The server keeps only a hash.
 */
import { computed, reactive, ref } from 'vue';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HCard,
  HCopyField,
  HDataTable,
  HDialog,
  HEmptyState,
  HHeading,
  HInput,
  HPageHeader,
  HSelect,
  HStack,
  HText,
  type MenuAction,
  type TableColumn,
  type TableRow,
} from '@hearth-ui/vue';
import type { User } from '@samyx/preview-stacks-client';
import { usersApi, type Token } from '../api-users';
import { byLabel } from '../lib/labels';
import { ago } from '../format';
import {
  LAST_ADMIN_WHY,
  MIN_PASSWORD,
  can,
  canCreateUser,
  isLastAdmin,
  pickerOptions,
  problem,
  rank,
  roleOptions,
  sentence,
  type Role,
} from '../lib/users';
import { auth, loadMe, toast } from '../state';

const users = ref<User[]>([]);
const tokens = ref<Token[]>([]);
const namedTokens = computed(() => byLabel(tokens.value, (t) => t.name, (t) => String(t.id)));
const loaded = ref(false);
const error = ref('');
const busy = ref(false);
const userError = ref('');
const tokenError = ref('');

const isAdmin = computed(() => can('admin', auth));
const isYou = (u: User) => !auth.root && u.id === auth.id;

const newUser = reactive<{ username: string; password: string; role: Role }>({ username: '', password: '', role: 'viewer' });
const newToken = ref('');
/** Held until dismissed. There is no way to show it again. */
const revealed = ref<{ name: string; value: string } | null>(null);

async function load(): Promise<void> {
  // The host token owns no personal tokens: asking would only log a 400.
  const [u, t] = await Promise.all([usersApi.list(), auth.root ? Promise.resolve(null) : usersApi.tokens()]);
  loaded.value = true;
  if (!u.ok) {
    error.value = problem(u, 'load the accounts');
    return;
  }
  error.value = '';
  users.value = u.body.users ?? [];
  tokens.value = t?.ok ? (t.body.tokens ?? []) : [];
}
void load();

// ── accounts ────────────────────────────────────────────────────────────────────────────────────
async function createUser(): Promise<void> {
  busy.value = true;
  userError.value = '';
  const r = await usersApi.create({ username: newUser.username.trim(), password: newUser.password, role: newUser.role });
  busy.value = false;
  if (!r.ok) {
    userError.value = problem(r, 'create the account');
    return;
  }
  toast(`Added ${r.body.user.username}`, 'success');
  Object.assign(newUser, { username: '', password: '', role: 'viewer' });
  void load();
}

/** Your own row, or any lowering of a role, waits for a confirm. */
const pendingRole = ref<{ user: User; role: string } | null>(null);
function askRole(u: User, role: string): void {
  if (role === u.role) return;
  if (isYou(u) || rank(role) < rank(u.role)) pendingRole.value = { user: u, role };
  else void changeRole(u, role);
}
async function confirmRole(): Promise<void> {
  const p = pendingRole.value;
  pendingRole.value = null;
  if (p) await changeRole(p.user, p.role);
}
async function changeRole(u: User, role: string): Promise<void> {
  if (role === u.role) return;
  const r = await usersApi.setRole(u.id, role);
  if (!r.ok) {
    error.value = problem(r, `change the role for ${u.username}`);
    void load();
    return;
  }
  error.value = '';
  if (isYou(u)) {
    // Every control here is drawn from the role you hold: re-read it before the next render.
    await loadMe();
    toast(`You are now ${role}`, 'success');
  } else toast(`${u.username} is now ${role}`, 'success');
  void load();
}

const del = reactive<{ user: User | null; token: Token | null }>({ user: null, token: null });
async function removeUser(): Promise<void> {
  const u = del.user;
  del.user = null;
  if (!u) return;
  const r = await usersApi.remove(u.id);
  if (!r.ok) {
    error.value = problem(r, `delete ${u.username}`);
    return;
  }
  toast(`Deleted ${u.username}`, 'success');
  void load();
}

const pw = reactive<{ user: User | null; value: string; busy: boolean; error: string }>({ user: null, value: '', busy: false, error: '' });
async function changePassword(): Promise<void> {
  const u = pw.user;
  if (!u || pw.busy || pw.value.length < MIN_PASSWORD) return;
  pw.busy = true;
  pw.error = '';
  const r = await usersApi.setPassword(u.id, pw.value);
  pw.busy = false;
  if (!r.ok) {
    // Stay open: the typed value is not lost and the reason is next to it.
    pw.error = problem(r, `change the password for ${u.username}`);
    return;
  }
  pw.user = null;
  pw.value = '';
  toast(isYou(u) ? 'Password changed. Sign in again.' : 'Password changed', 'success');
  void load();
}

// ── tokens ──────────────────────────────────────────────────────────────────────────────────────
async function createToken(): Promise<void> {
  const name = newToken.value.trim();
  busy.value = true;
  tokenError.value = '';
  const r = await usersApi.createToken(name);
  busy.value = false;
  if (!r.ok) {
    tokenError.value = problem(r, 'create the token');
    return;
  }
  revealed.value = { name, value: r.body.token };
  newToken.value = '';
  void load();
}
async function removeToken(): Promise<void> {
  const t = del.token;
  del.token = null;
  if (!t) return;
  const r = await usersApi.removeToken(t.id);
  if (!r.ok) {
    error.value = problem(r, `revoke ${t.name}`);
    return;
  }
  toast(`Revoked ${t.name}`, 'success');
  void load();
}

// ── tables ──────────────────────────────────────────────────────────────────────────────────────
const userCols: TableColumn[] = [
  { key: 'username', label: 'User', truncate: true },
  { key: 'role', label: 'Role', minWidth: '10rem' },
  { key: 'added', label: 'Added', align: 'end', hideBelow: 520 },
];
const userRows = computed<TableRow[]>(() =>
  users.value.map((u) => ({ id: u.username, username: u.username, role: u.role, added: ago(u.createdAt) })),
);
const userById = (row: TableRow) => users.value.find((u) => u.username === row.id);

const userActions = (row: TableRow): MenuAction[] => {
  const u = userById(row);
  if (!u) return [];
  const out: MenuAction[] = [];
  if (isYou(u) || isAdmin.value) out.push({ id: 'password', label: 'Change password', icon: 'lock' });
  if (isAdmin.value) out.push({ id: 'delete', label: 'Delete', icon: 'close', danger: true, disabled: isLastAdmin(u, users.value), separatorBefore: out.length > 0 });
  return out;
};
function onUserAction(e: { id: string; action: string }): void {
  const u = users.value.find((x) => x.username === e.id);
  if (!u) return;
  if (e.action === 'password') Object.assign(pw, { user: u, value: '', error: '' });
  else if (e.action === 'delete') del.user = u;
}

const tokenCols: TableColumn[] = [
  { key: 'name', label: 'Name', truncate: true },
  { key: 'used', label: 'Last used', align: 'end' },
];
const tokenRows = computed<TableRow[]>(() =>
  [...namedTokens.value].map(([rid, t]) => ({ id: rid, name: t.name, used: t.lastUsedAt ? ago(t.lastUsedAt) : 'never' })),
);
const tokenActions = (): MenuAction[] => [{ id: 'revoke', label: 'Revoke', danger: true }];
function onTokenAction(e: { id: string }): void {
  del.token = namedTokens.value.get(e.id) ?? null;
}

const roleOpen = computed({ get: () => !!pendingRole.value, set: (v) => !v && (pendingRole.value = null) });
const delOpen = computed({ get: () => !!del.user, set: (v) => !v && (del.user = null) });
const tokOpen = computed({ get: () => !!del.token, set: (v) => !v && (del.token = null) });
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Users" description="Accounts and your API tokens." actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" @click="load" />
      </template>
    </HPageHeader>

    <HAlert v-if="error" tone="danger" title="Not done" :description="error" dismissible @dismiss="error = ''" />

    <HCard title="Accounts" :description="loaded ? `${users.length} on this host.` : undefined">
      <HStack direction="column" :gap="4">
        <HDataTable
          label="Accounts"
          :rows="userRows"
          :columns="userCols"
          :loading="!loaded"
          :get-row-actions="userActions"
          empty-text="No accounts yet."
          @row-action="onUserAction"
        >
          <template #cell="{ row, column, value }">
            <HStack direction="row" v-if="column.key === 'username'" :gap="2" align="center" wrap>
              <HText weight="medium" style="min-width: 0; overflow: hidden; text-overflow: ellipsis">{{ value }}</HText>
              <HBadge v-if="userById(row) && isYou(userById(row)!)" label="you" tone="info" size="sm" />
            </HStack>
            <span v-else-if="column.key === 'role' && isAdmin && userById(row)" :title="isLastAdmin(userById(row)!, users) ? LAST_ADMIN_WHY : undefined">
              <HSelect
                :label="`Role for ${row.username}`"
                hide-label
                size="compact"
                :model-value="String(value)"
                :options="pickerOptions(String(value))"
                :disabled="isLastAdmin(userById(row)!, users)"
                @update:model-value="(v: string) => askRole(userById(row)!, v)"
              />
            </span>
            <template v-else-if="column.key === 'role'">{{ sentence(String(value)) }}</template>
            <template v-else>{{ value }}</template>
          </template>
        </HDataTable>

        <form v-if="isAdmin" @submit.prevent="createUser">
          <HStack direction="column" :gap="3">
            <HHeading :level="3" size="sm">Add an account</HHeading>
            <HStack direction="row" :gap="3" wrap align="end">
              <div class="field"><HInput v-model="newUser.username" label="Username" autocomplete="off" /></div>
              <div class="field"><HInput v-model="newUser.password" label="Password" type="password" autocomplete="new-password" :placeholder="`${MIN_PASSWORD}+ characters`" /></div>
              <div class="field"><HSelect v-model="newUser.role" label="Role" :options="roleOptions" /></div>
              <HButton type="submit" variant="primary" label="Add account" :loading="busy" :disabled="!canCreateUser(newUser)" />
            </HStack>
            <HText v-if="newUser.password && newUser.password.length < MIN_PASSWORD" tone="muted" size="sm">The password needs at least {{ MIN_PASSWORD }} characters.</HText>
            <HAlert v-if="userError" tone="danger" :description="userError" />
          </HStack>
        </form>
        <HText v-else tone="muted" size="sm">Admin only: adding accounts and changing roles.</HText>
      </HStack>
    </HCard>

    <HCard title="Your API tokens">
      <HStack direction="column" :gap="4">
        <HDataTable
          v-if="tokens.length || !loaded"
          label="API tokens"
          :rows="tokenRows"
          :columns="tokenCols"
          :loading="!loaded"
          :get-row-actions="tokenActions"
          @row-action="onTokenAction"
        />
        <HEmptyState v-else :title="auth.root ? 'The host token has no personal tokens.' : 'No tokens yet.'" icon="lock" />

        <form v-if="!auth.root" @submit.prevent="createToken">
          <HStack direction="row" :gap="3" wrap align="end">
            <div class="field"><HInput v-model="newToken" label="Token name" placeholder="ci-deploy" autocomplete="off" /></div>
            <HButton type="submit" variant="primary" label="Create token" :loading="busy" :disabled="!newToken.trim()" />
          </HStack>
          <HAlert v-if="tokenError" tone="danger" :description="tokenError" />
        </form>
      </HStack>
    </HCard>

    <HDialog
      :open="!!pw.user"
      :title="`Change password for ${pw.user?.username ?? ''}`"
      :description="`Ends ${pw.user && isYou(pw.user) ? 'your' : 'their'} sessions and revokes ${pw.user && isYou(pw.user) ? 'your' : 'their'} tokens.`"
      @close="pw.user = null"
    >
      <form @submit.prevent="changePassword">
        <HStack direction="column" :gap="3">
          <HInput v-model="pw.value" label="New password" type="password" autocomplete="new-password" :hint="`At least ${MIN_PASSWORD} characters.`" />
          <HAlert v-if="pw.error" tone="danger" :description="pw.error" />
        </HStack>
      </form>
      <template #footer>
        <HButton variant="ghost" label="Cancel" @click="pw.user = null" />
        <HButton variant="primary" label="Change password" :loading="pw.busy" :disabled="pw.value.length < MIN_PASSWORD" @click="changePassword" />
      </template>
    </HDialog>

    <HDialog :open="!!revealed" :title="`Token ${revealed?.name ?? ''}`" description="Shown once. It can do anything your account can." @close="revealed = null">
      <HCopyField v-if="revealed" label="Token" :value="revealed.value" secret />
      <template #footer>
        <HButton variant="primary" label="Done" @click="revealed = null" />
      </template>
    </HDialog>

    <HAlertDialog
      v-model:open="roleOpen"
      :danger="pendingRole ? isYou(pendingRole.user) : false"
      :title="pendingRole ? (isYou(pendingRole.user) ? `Make yourself ${pendingRole.role}?` : `Make ${pendingRole.user.username} ${pendingRole.role}?`) : ''"
      description="Takes effect now."
      confirm-label="Change role"
      @confirm="confirmRole"
    />
    <HAlertDialog v-model:open="delOpen" danger :title="`Delete ${del.user?.username ?? ''}?`" description="The account and its tokens are removed." confirm-label="Delete" @confirm="removeUser" />
    <HAlertDialog v-model:open="tokOpen" danger :title="`Revoke ${del.token?.name ?? ''}?`" description="Anything using it stops working." confirm-label="Revoke" @confirm="removeToken" />
  </HStack>
</template>

<style scoped>
/* A wrapper is the flex item: HInput and HSelect pass style to their inner control. Fields fill the row
   evenly, and a wrapped one fills the card. */
.field {
  flex: 1 1 12rem;
  min-width: 0;
}
</style>
