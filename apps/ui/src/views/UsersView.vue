<script setup lang="ts">
/**
 * Accounts and personal API tokens.
 *
 * Accounts landed in 0.10.0 with no UI at all, so the only way to add a second operator was a curl
 * call — which is exactly the thing a control plane exists to spare people.
 *
 * TWO AUDIENCES, TWO SECTIONS, and the split is deliberate. **Accounts** are about other people and
 * are shared state; **tokens** are yours alone (the API scopes them to the caller and cannot show
 * anyone else's). Mixing them into one table would imply an admin can manage another person's
 * tokens, which is not true and would be a bad idea if it were.
 *
 * SECRETS ARE SHOWN ONCE, and the page says so in the same breath as showing them. Same discipline
 * as a notifier signing secret and a registry credential: there is no read path on the server, so
 * this is not a policy that could be relaxed later — the value genuinely stops existing.
 *
 * ── ROLES ────────────────────────────────────────────────────────────────────────────────────────
 *
 * The roster is ordinary team information, so ANY signed-in account reads this page. Changing it —
 * adding an account, setting a role, deleting one, setting someone ELSE's password — is admin's,
 * and those controls are simply not rendered for anyone else. Your OWN password stays yours at
 * every role, so that one button is there for everybody, on their own row only.
 *
 * Hiding is UX. `can('admin')` decides what to DRAW; the server's permission table decides what is
 * allowed, and a hidden control that is somehow reached still comes back 403 — which lands in
 * `listError` and says which role the route wanted. That path is not a fallback, it is the actual
 * enforcement, so it stays.
 *
 * THE LAST ADMIN is the one rule the server has that the UI can see coming: it refuses to delete or
 * demote the final admin account. A request that can only fail is worse than a control that says
 * why it cannot be pressed, so that row's role picker and Delete arrive already disabled, carrying
 * the same remedy the server would have answered with — promote another account first.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import { ROLES, type Role, type User, type UsersResponse } from '../api/types';
import { authState, can, checkAuth } from '../composables/useAuth';
import { sentence } from '../composables/useFormat';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import EquivalentCommand from '../components/EquivalentCommand.vue';
import ErrorNote from '../components/ErrorNote.vue';
import InfoHint from '../components/InfoHint.vue';
import RelativeTime from '../components/RelativeTime.vue';
import SkeletonList from '../components/SkeletonList.vue';
import RefreshButton from '../components/RefreshButton.vue';
import SelectMenu from '../components/SelectMenu.vue';

type Token = { id: number; name: string; createdAt: number; lastUsedAt: number | null };

const users = ref<User[]>([]);
const tokens = ref<Token[]>([]);
const loaded = ref(false);
const listError = ref('');

const newUser = ref<{ username: string; password: string; role: Role }>({
  username: '',
  password: '',
  role: 'viewer',
});
const newToken = ref('');
const pwFor = ref<User | null>(null);
const pwValue = ref('');

/** Held only until dismissed — there is no way to show either of these again. */
const revealed = ref<{ what: string; value: string; note: string } | null>(null);

const canCreateUser = computed(
  () => newUser.value.username.trim().length > 0 && newUser.value.password.length >= 8,
);

/** One line each, in the picker itself — a role name alone does not say what it costs to grant. */
const ROLE_WHAT: Record<Role, string> = {
  viewer: 'reads everything',
  developer: 'deploys and tears down',
  maintainer: 'host configuration',
  admin: 'accounts and sign-on',
};
const roleOptions = ROLES.map((r) => ({ value: r, label: sentence(r), hint: ROLE_WHAT[r] }));

const adminCount = computed(() => users.value.filter((u) => u.role === 'admin').length);
/**
 * The last admin cannot be demoted or deleted — the server refuses both, and a host with no admin
 * has nobody who can add one back. Counting ROWS is the same thing the server counts: root holds
 * PSTACK_TOKEN, not an account, and may not exist as a human at all.
 */
function lastAdmin(u: User): boolean {
  return u.role === 'admin' && adminCount.value === 1;
}
const LAST_ADMIN_WHY = 'The only admin — promote another account first.';

async function load(): Promise<void> {
  const [u, t] = await Promise.all([
    api.get<UsersResponse>('/api/users'),
    api.get<{ tokens: Token[] }>('/api/tokens'),
  ]);
  loaded.value = true;
  if (!u.ok) {
    listError.value = problem(u, 'load the accounts');
    return;
  }
  listError.value = '';
  users.value = u.body.users ?? [];
  // Root (PSTACK_TOKEN) is not a user, so it has no personal tokens — an error here is expected.
  tokens.value = t.ok ? (t.body.tokens ?? []) : [];
}
void load();

async function createUser(): Promise<void> {
  // The role is always sent, never left to the server's default: what this form creates should be
  // whatever the form is showing. (An absent role means viewer there, which is the same answer —
  // but the day it is not, this page would be creating something other than what it said.)
  const r = await api.post<{ user: User }>('/api/users', {
    username: newUser.value.username.trim(),
    password: newUser.value.password,
    role: newUser.value.role,
  });
  if (!r.ok) {
    listError.value = problem(r, 'create this account');
    return;
  }
  toast('ok', `Created ${r.body.user.username} as a ${r.body.user.role}.`);
  newUser.value = { username: '', password: '', role: 'viewer' };
  void load();
}

async function removeUser(u: User): Promise<void> {
  const r = await api.del(`/api/users/${u.id}`);
  // The server refuses to delete the last account. That is a rule worth surfacing verbatim rather
  // than as "could not delete" — it tells the operator what to do instead.
  if (!r.ok) {
    listError.value = problem(r, `delete ${u.username}`);
    return;
  }
  toast('ok', `Deleted ${u.username}.`);
  void load();
}

async function changeRole(u: User, role: string): Promise<void> {
  if (role === u.role) return;
  const r = await api.patch(`/api/users/${u.id}`, { role });
  if (!r.ok) {
    listError.value = problem(r, `change the role for ${u.username}`);
    return;
  }
  listError.value = '';
  if (u.id === authState.user?.id) {
    // You just changed your OWN role, and this whole app — the rail, this page's controls, the
    // footer — is drawn from the role it is holding. Re-read it before anything renders again,
    // or the UI keeps offering what the server will now refuse. (The server needs no prompting:
    // every request looks the role up fresh.)
    await checkAuth();
    toast('ok', `You are now a ${role}. What this app offers you changed with it.`);
  } else {
    toast('ok', `${u.username} is now a ${role}.`);
  }
  void load();
}

async function changePassword(): Promise<void> {
  const u = pwFor.value;
  if (!u) return;
  const r = await api.put(`/api/users/${u.id}/password`, { password: pwValue.value });
  if (!r.ok) {
    listError.value = problem(r, `change the password for ${u.username}`);
    return;
  }
  pwFor.value = null;
  pwValue.value = '';
  // Not a footnote: the change signs that user out everywhere, and if it was YOU, the next request
  // is a 401. Saying so here is the difference between an expected step and an apparent bug.
  toast(
    'ok',
    u.id === authState.user?.id
      ? `Password changed. You have been signed out everywhere — sign in again.`
      : `Password changed. ${u.username} has been signed out everywhere.`,
  );
  void load();
}

async function createToken(): Promise<void> {
  const r = await api.post<{ token: string; id: number }>('/api/tokens', {
    name: newToken.value.trim(),
  });
  if (!r.ok) {
    listError.value = problem(r, 'create this token');
    return;
  }
  revealed.value = {
    what: `Personal token “${newToken.value.trim()}”`,
    value: r.body.token,
    note: 'Send it as a bearer token. The server stores only a hash, so this is the only time it is shown.',
  };
  newToken.value = '';
  void load();
}

async function removeToken(t: Token): Promise<void> {
  const r = await api.del(`/api/tokens/${t.id}`);
  if (!r.ok) {
    listError.value = problem(r, `revoke ${t.name}`);
    return;
  }
  toast('ok', `Revoked ${t.name}.`);
  void load();
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Users &amp; access</h1>
        <div class="sub">
          Who can sign in, and the tokens they use from scripts
          <InfoHint label="how access works here">
            Three kinds of caller. The host token from the environment is the root credential CI
            holds. An account signs in with a password and gets a browser session. A personal token
            belongs to one account and is what a script uses — it can do anything that account can.
          </InfoHint>
        </div>
      </div>
      <span class="grow" />
      <RefreshButton :run="load" />
    </div>

    <ErrorNote v-if="listError" :text="listError" title="Something went wrong." />

    <div v-if="revealed" class="banner ok">
      <b>{{ revealed.what }} — copy it now.</b>
      <p>{{ revealed.note }}</p>
      <pre class="code">{{ revealed.value }}</pre>
      <div class="row">
        <button @click="revealed = null">I have stored it</button>
      </div>
    </div>

    <div class="grid-2">
      <section class="panel">
        <div class="phead">
          <h2 class="section">Accounts</h2>
          <span class="grow" />
          <span class="mute" style="font-size: var(--t-xs)">{{ users.length }}</span>
        </div>

        <SkeletonList v-if="!loaded" :rows="3" />
        <!-- `cards` + `data-label` per cell: below 640px the same markup renders as cards. Without
             it the row's two buttons pushed the table past the viewport and got clipped. -->
        <table v-else-if="users.length" class="cards">
          <thead>
            <tr>
              <th>User</th>
              <th>Role</th>
              <th>Added</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.id">
              <td class="name" data-label="user">
                <b>{{ u.username }}</b>
                <span v-if="u.id === authState.user?.id" class="badge info">you</span>
              </td>
              <!-- An admin gets the picker in place: promotion is a one-step decision about one
                   person, and a dialog for a four-option choice is a page-blocking ceremony. Anyone
                   else reads the role as text — `sentence()` at render only, never on the value.
                   A role this build does not know still shows: it falls through to `placeholder`. -->
              <td data-label="role">
                <span
                  v-if="can('admin')"
                  class="role-pick"
                  :title="lastAdmin(u) ? LAST_ADMIN_WHY : undefined"
                >
                  <SelectMenu
                    :model-value="u.role"
                    :label="`Role for ${u.username}`"
                    :options="roleOptions"
                    :disabled="lastAdmin(u)"
                    :placeholder="sentence(u.role)"
                    @update:model-value="(v) => changeRole(u, v)"
                  />
                </span>
                <span v-else>{{ sentence(u.role) }}</span>
              </td>
              <td class="dim nowrap" data-label="added"><RelativeTime :at="u.createdAt" /></td>
              <td class="right nowrap" data-label="">
                <!-- Your own password is yours at any role; someone else's is an admin operation. -->
                <button
                  v-if="u.id === authState.user?.id || can('admin')"
                  class="ghost sm"
                  @click="pwFor = u; pwValue = ''"
                >
                  Change password
                </button>
                <ActionButton
                  v-if="can('admin')"
                  class="danger sm"
                  :confirm="`Delete ${u.username}?`"
                  :disabled="lastAdmin(u)"
                  :title="lastAdmin(u) ? LAST_ADMIN_WHY : undefined"
                  @run="removeUser(u)"
                  >Delete</ActionButton
                >
              </td>
            </tr>
          </tbody>
        </table>
        <p v-else class="hint">No accounts yet.</p>

        <form v-if="can('admin')" class="stack-form" @submit.prevent="createUser">
          <h3>Add an account</h3>
          <div class="row wrap two-up">
            <label class="field grow">
              <span>Username</span>
              <input v-model="newUser.username" type="text" autocomplete="off" />
            </label>
            <label class="field grow">
              <span>Password</span>
              <input v-model="newUser.password" type="password" autocomplete="new-password" />
            </label>
          </div>
          <div class="field">
            <label for="new-role">Role</label>
            <SelectMenu
              id="new-role"
              :model-value="newUser.role"
              label="Role for the new account"
              :options="roleOptions"
              @update:model-value="(v) => (newUser.role = v as Role)"
            />
          </div>
          <p class="hint">At least 8 characters. A new account starts as a viewer unless you say otherwise.</p>
          <div class="row">
            <button class="primary" type="submit" :disabled="!canCreateUser">Add account</button>
            <span class="grow" />
          </div>
          <EquivalentCommand
            what="this account creation"
            method="POST"
            path="/api/users"
            :body="{ username: newUser.username || 'alice', password: '…', role: newUser.role }"
          />
        </form>
        <!-- Not an apology for a missing button: it says who to ask, which is the thing the reader
             actually needs. Reading the roster is every account's; changing it is admin's. -->
        <p v-else class="hint">
          Only an admin can add an account or change a role. Ask one of the admins above.
        </p>
      </section>

      <section class="panel">
        <div class="phead">
          <h2 class="section">Your API tokens</h2>
          <InfoHint label="when to use one">
            A script or a CI job signing in as you. It carries whatever you can do, so give each one
            a name you will recognise later and revoke the ones you stop using.
          </InfoHint>
        </div>

        <SkeletonList v-if="!loaded" :rows="2" />
        <table v-else-if="tokens.length" class="cards">
          <thead>
            <tr>
              <th>Name</th>
              <th>Last used</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in tokens" :key="t.id">
              <td class="name" data-label="name"><b>{{ t.name }}</b></td>
              <td class="dim nowrap" data-label="last used">
                <RelativeTime v-if="t.lastUsedAt" :at="t.lastUsedAt" />
                <span v-else class="mute">never</span>
              </td>
              <td class="right nowrap" data-label="">
                <ActionButton
                  class="danger sm"
                  :confirm="`Revoke ${t.name}?`"
                  @run="removeToken(t)"
                  >Revoke</ActionButton
                >
              </td>
            </tr>
          </tbody>
        </table>
        <p v-else class="hint">
          {{ authState.root ? 'The host token is not an account, so it has no personal tokens.' : 'No tokens yet.' }}
        </p>

        <form v-if="!authState.root" class="stack-form" @submit.prevent="createToken">
          <h3>New token</h3>
          <label class="field">
            <span>What is it for</span>
            <input v-model="newToken" type="text" placeholder="ci-deploy" autocomplete="off" />
          </label>
          <div class="row">
            <button class="primary" type="submit" :disabled="!newToken.trim()">Create token</button>
          </div>
          <EquivalentCommand
            what="this token creation"
            method="POST"
            path="/api/tokens"
            :body="{ name: newToken || 'ci-deploy' }"
          />
        </form>
      </section>
    </div>

    <!-- Password change: a dialog, because it is a distinct decision about one row. -->
    <Transition name="fade">
      <div v-if="pwFor" class="scrim" @click.self="pwFor = null">
        <div class="sheet" role="dialog" aria-modal="true" aria-label="Change password">
          <h3>Change password for {{ pwFor.username }}</h3>
          <p class="hint">
            This signs {{ pwFor.id === authState.user?.id ? 'you' : 'them' }} out everywhere —
            every session and personal token is revoked.
          </p>
          <label class="field">
            <span>New password</span>
            <input v-model="pwValue" type="password" autocomplete="new-password" />
          </label>
          <div class="row">
            <button class="primary" :disabled="pwValue.length < 8" @click="changePassword">
              Change password
            </button>
            <button class="ghost" @click="pwFor = null">Cancel</button>
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.stack-form {
  margin-top: var(--s5);
  padding-top: var(--s4);
  border-top: 1px solid var(--line-soft);
  display: flex;
  flex-direction: column;
  gap: var(--s3);
}
.stack-form h3 {
  font-size: var(--t-sm);
  color: var(--fg-dim);
}
td.right {
  text-align: end;
}
/* Two fields share a line where there is room and stack where there is not — at 390px a pair of
 * half-width inputs is two inputs nobody can type into. */
.two-up > .field {
  flex: 1 1 200px;
}
td.right button {
  margin-inline-start: var(--s2);
}
/* The picker sits in a row, not a form: it takes the width its longest label needs and no more,
 * so the Role column does not eat the table. The wrapper exists to carry `title` when the control
 * inside it is disabled — a disabled button does not reliably show one of its own. */
.role-pick {
  display: inline-block;
}
.role-pick :deep(.selm-trigger) {
  width: auto;
  min-width: 150px;
}
</style>
