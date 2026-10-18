<script setup lang="ts">
/**
 * Single sign-on providers. Three exclusive states: the list, the preset picker, one provider form.
 *
 * What the form must not get wrong:
 *  1. The callback URL is read-only and comes from the server: it must match the provider's
 *     registered one byte for byte.
 *  2. The client secret is write-only. On an existing provider an empty field keeps the stored one.
 *  3. A template preset's issuer (https://<your-domain>.okta.com/...) is assembled from its
 *     placeholders; the server refuses the template verbatim.
 *  4. The role new accounts get is on screen and rests on "host default", never on admin.
 */
import { computed, nextTick, ref, watch } from 'vue';
import {
  HAlert,
  HAlertDialog,
  HBadge,
  HButton,
  HCard,
  HCollapsible,
  HCopyField,
  HEmptyState,
  HHeading,
  HInput,
  HLink,
  HPageHeader,
  HSelect,
  HSkeleton,
  HStack,
  HSurface,
  HSwitch,
  HText,
} from '@hearth-ui/vue';
import type { SsoPreset, SsoProviderEntry } from '@samyx/preview-stacks-client';
import { ssoApi } from '../api-sso';
import SsoMark from '../components/SsoMark.vue';
import { ago } from '../format';
import {
  INHERIT,
  assembled,
  buildBody,
  cannotSave,
  pickerTiles,
  placeholderNames,
  roleLine,
  rulesLine,
  startAdd,
  startEdit,
  templateParts,
  whereLine,
  type Editing,
  type Tile,
} from '../lib/sso';
import { ROLES, ROLE_WHAT, can, problem, sentence } from '../lib/users';
import { auth, toast } from '../state';

const providers = ref<SsoProviderEntry[]>([]);
const presets = ref<SsoPreset[]>([]);
const callbackUrl = ref('');
const hostDefault = ref('');
const loaded = ref(false);
const error = ref('');
const forbidden = ref(false);
/** Courtesy: maintainers read the settings, only admins change them. The server still decides. */
const admin = computed(() => can('admin', auth));

const editing = ref<Editing | null>(null);
const picking = ref(false);
const saving = ref(false);
const formError = ref('');
const removing = ref<SsoProviderEntry | null>(null);

async function load(): Promise<void> {
  const r = await ssoApi.config();
  loaded.value = true;
  forbidden.value = r.status === 403;
  if (!r.ok) {
    error.value = forbidden.value ? '' : problem(r, 'read the sign-on settings');
    return;
  }
  error.value = '';
  providers.value = r.body.providers ?? [];
  presets.value = r.body.presets ?? [];
  callbackUrl.value = r.body.callbackUrl;
}
void load();
void ssoApi.hostDefaultRole().then((v) => (hostDefault.value = v));

/** With nothing configured the picker is the page. */
const showPicker = computed(() => admin.value && !editing.value && (picking.value || (loaded.value && !error.value && !forbidden.value && providers.value.length === 0)));
const tiles = computed(() => pickerTiles(presets.value));
const keys = computed(() => providers.value.map((p) => p.key));

const parts = computed(() => (editing.value ? templateParts(editing.value) : null));
const names = computed(() => placeholderNames(parts.value));
const issuer = computed(() => (parts.value && editing.value ? assembled(parts.value, editing.value.placeholders) : ''));
const blocked = computed(() => (editing.value ? cannotSave(editing.value, keys.value) : ''));

const roleOptions = computed(() => [
  { value: INHERIT, label: `Host default${hostDefault.value ? ` (${sentence(hostDefault.value)})` : ''}` },
  ...ROLES.map((r) => ({ value: r, label: `${sentence(r)} - ${ROLE_WHAT[r]}` })),
]);
const roleChoice = computed<string>({
  get: () => editing.value?.form.defaultRole || INHERIT,
  set: (v) => {
    if (editing.value) editing.value.form.defaultRole = v === INHERIT ? '' : v;
  },
});

const title = computed(() => {
  const e = editing.value;
  if (!e) return '';
  if (e.originalKey) return `Edit ${e.originalKey}`;
  return `Add ${e.preset?.label ?? (e.form.mode === 'oidc' ? 'an OpenID Connect issuer' : 'a custom OAuth 2.0 provider')}`;
});

function add(t: Tile): void {
  editing.value = startAdd(t, keys.value);
  formError.value = '';
  picking.value = false;
}
function edit(p: SsoProviderEntry): void {
  editing.value = startEdit(p, presets.value);
  formError.value = '';
  picking.value = false;
}

async function save(): Promise<void> {
  const e = editing.value;
  if (!e) return;
  saving.value = true;
  const body = buildBody(e);
  const r = await ssoApi.save(body);
  saving.value = false;
  if (!r.ok) {
    // Verbatim: the refusals are written to be read (the scope refusal names the scope).
    formError.value = problem(r, 'save the provider');
    return;
  }
  formError.value = '';
  toast(e.originalKey ? 'Saved' : `Added ${body.key}`, 'success');
  editing.value = null;
  void load();
}

async function remove(): Promise<void> {
  const p = removing.value;
  removing.value = null;
  if (!p) return;
  const r = await ssoApi.remove(p.key);
  if (!r.ok) {
    toast(problem(r, 'remove the provider'), 'danger');
    return;
  }
  toast(`Removed ${p.key}`, 'success', 'Its accounts keep working.');
  void load();
}

// A branch swap removes the focused element: land on the new view so keyboard and screen-reader users follow it.
const stage = ref<HTMLElement | null>(null);
watch([editing, picking], () => nextTick(() => stage.value?.focus()));

const removeOpen = computed({ get: () => !!removing.value, set: (v) => !v && (removing.value = null) });
const claimKeys = ['subject', 'username', 'email', 'name', 'avatar'] as const;
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Single sign-on" description="Sign in with the identity providers you already run." actions-placement="end">
      <template #actions>
        <HButton v-if="admin && loaded && !error && !editing && !showPicker" variant="primary" icon="plus" label="Add provider" @click="picking = true" />
      </template>
    </HPageHeader>

    <HAlert v-if="error" tone="danger" title="Not done" :description="error">
      <HButton variant="secondary" size="compact" icon="arrow" label="Retry" @click="load" />
    </HAlert>
    <HEmptyState v-if="forbidden" icon="lock" title="Maintainer only" description="Your account cannot read sign-on settings." />
    <div v-else ref="stage" tabindex="-1" class="stage">
    <HSkeleton v-if="!loaded" variant="rectangle" height="8rem" label="Loading providers" />

    <!-- the form: one provider -->
    <form v-else-if="editing" @submit.prevent="save">
      <HStack direction="column" :gap="4">
      <HCard :title="title" :description="editing.form.mode === 'oidc' ? 'OpenID Connect' : 'OAuth 2.0'">
        <HStack direction="column" :gap="4">
          <HStack direction="row" :gap="3" align="center" wrap>
            <SsoMark :preset="editing.form.provider" :size="22" />
            <HSwitch v-model="editing.form.enabled" label="Enabled" />
            <HLink v-if="editing.preset" :href="editing.preset.setupUrl" target="_blank">Create the app in {{ editing.preset.label }}</HLink>
          </HStack>

          <HCopyField label="Redirect URI" :value="callbackUrl" />
          <HText tone="muted" size="sm">Must match the provider's registered callback exactly.</HText>

          <HInput v-if="!editing.originalKey" v-model="editing.key" label="Key" hint="Lowercase letters, digits, dashes." />
          <HInput v-model="editing.form.label" label="Button text" :placeholder="editing.preset?.label || 'Acme SSO'" :hint="`Continue with ${editing.form.label || editing.preset?.label || '...'}`" />

          <template v-if="editing.form.mode === 'oidc'">
            <template v-if="parts">
              <HInput v-for="ph in names" :key="ph" v-model="editing.placeholders[ph]" :label="sentence(ph)" />
              <HCopyField label="Issuer" :value="issuer" />
            </template>
            <HInput v-else v-model="editing.form.discoveryUrl" label="Issuer" placeholder="https://accounts.google.com" hint="Fetched when you save." />
          </template>

          <HInput v-model="editing.form.clientId" label="Client id" autocomplete="off" required />
          <HInput
            v-model="editing.secret"
            label="Client secret"
            type="password"
            autocomplete="new-password"
            :hint="editing.secretSet ? 'Write-only. Leave empty to keep the stored secret.' : 'Write-only. Never returned once saved.'"
          />
          <HInput v-model="editing.form.scopes" label="Scopes" :placeholder="editing.form.mode === 'oidc' ? 'openid profile email' : ''" />

          <HCollapsible v-if="editing.form.mode === 'oauth2'" label="Endpoints and claim mapping" :default-open="!editing.preset">
            <HStack direction="column" :gap="3">
              <HInput v-model="editing.form.authorizeUrl" label="Authorize URL" />
              <HInput v-model="editing.form.tokenUrl" label="Token URL" />
              <HInput v-model="editing.form.userInfoUrl" label="User info URL" />
              <HInput v-model="editing.form.emailsUrl" label="Emails URL (optional)" />
              <HInput v-model="editing.form.groupsUrl" label="Groups URL (optional)" />
              <HHeading :level="3" size="sm">Claim mapping</HHeading>
              <HStack direction="row" :gap="3" wrap>
                <HInput v-for="k in claimKeys" :key="k" v-model="editing.form.claimMap[k]" :label="sentence(k)" />
              </HStack>
            </HStack>
          </HCollapsible>
        </HStack>
      </HCard>

      <HCard title="Who gets in" description="Every filled list must match. Empty lists restrict nobody.">
        <HStack direction="column" :gap="4">
          <HInput v-model="editing.domains" label="Allowed email domains" placeholder="example.com, corp.example" hint="Comma-separated; subdomains count. A login with no address is refused." />
          <HInput v-model="editing.usernames" label="Allowed usernames" placeholder="octocat, qa-[0-9]*" hint="Comma-separated patterns: * ? [0-9]. A login with no username is refused." />
          <HInput
            v-if="editing.form.mode === 'oauth2'"
            v-model="editing.groups"
            label="Required groups"
            placeholder="acme, acme/backend"
            hint="Exact names, not patterns. Needs the matching scope, or the save is refused."
          />
          <HSelect v-model="roleChoice" label="New account role" :options="roleOptions" hint="Set at first sign-in. The host default is in Settings." />
        </HStack>
      </HCard>

      <HAlert v-if="formError" tone="danger" title="Not saved" :description="formError" />
      <HText v-if="blocked" tone="muted" size="sm">{{ blocked }}</HText>
      <HStack direction="row" :gap="3">
        <HButton type="submit" variant="primary" :loading="saving" :disabled="saving || !!blocked" :label="editing.originalKey ? 'Save changes' : 'Add provider'" />
        <HButton variant="ghost" label="Cancel" @click="editing = null" />
      </HStack>
      </HStack>
    </form>

    <!-- the picker -->
    <HCard v-else-if="showPicker" :title="providers.length ? 'Add a provider' : 'Pick a provider'" description="Where your team already signs in.">
      <HStack direction="column" :gap="3">
        <div class="tiles">
          <HButton v-for="t in tiles" :key="t.id" variant="secondary" :aria-label="`${t.label}, ${t.mode === 'oidc' ? 'OpenID Connect' : 'OAuth 2.0'}`" @click="add(t)">
            <template #icon><SsoMark :preset="t.preset?.key ?? 'custom'" :size="20" /></template>
            {{ t.label }}
          </HButton>
        </div>
        <HStack direction="row" v-if="providers.length"><HButton variant="ghost" label="Cancel" @click="picking = false" /></HStack>
      </HStack>
    </HCard>

    <!-- the list -->
    <template v-else-if="!error">
      <HStack direction="column" :gap="3">
        <HSurface v-for="p in providers" :key="p.key" bordered padding="md" as="article">
          <HStack direction="row" :gap="3" align="center" wrap>
            <SsoMark :preset="p.config.provider" :size="22" />
            <HStack direction="column" :gap="1" style="flex: 1 1 14rem; min-width: 0">
              <HStack direction="row" :gap="2" align="center" wrap>
                <HText weight="bold">{{ p.config.label }}</HText>
                <HText as="code" tone="muted" size="sm">{{ p.key }}</HText>
                <HBadge v-if="!p.config.enabled" label="disabled" tone="neutral" size="sm" />
              </HStack>
              <HText tone="muted" size="sm" class="wrap">
                {{ whereLine(p.config, presets) }}. {{ rulesLine(p.config) }}. {{ roleLine(p.config, hostDefault) }}. Updated {{ ago(p.updatedAt) }}.
              </HText>
            </HStack>
            <HStack v-if="admin" direction="row" :gap="2">
              <HButton size="compact" variant="secondary" label="Edit" :aria-label="`Edit ${p.key}`" @click="edit(p)" />
              <HButton size="compact" variant="danger" label="Remove" :aria-label="`Remove ${p.key}`" @click="removing = p" />
            </HStack>
          </HStack>
        </HSurface>
      </HStack>
      <HCard title="Callback URL" description="Register this with every provider.">
        <HCopyField label="Callback URL" :value="callbackUrl" />
      </HCard>
    </template>

    </div>

    <HAlertDialog v-model:open="removeOpen" danger :title="`Remove ${removing?.key ?? ''}?`" description="Its accounts keep working." confirm-label="Remove" @confirm="remove" />
  </HStack>
</template>

<style scoped>
.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr));
  gap: var(--h-space-3, 0.75rem);
}
.stage {
  display: flex;
  flex-direction: column;
  gap: var(--h-space-4, 1rem);
  outline: none;
}
.wrap {
  overflow-wrap: anywhere;
}
</style>
