<script setup lang="ts">
/**
 * Single sign-on: the identity providers this host lets people sign in with.
 *
 * THE POINT OF THE PAGE is that nobody has to create accounts here any more. Whoever can
 * authenticate against the operator's directory can sign in, and the account appears on first
 * login. The provider keeps owning who is allowed — Google Workspace restricts to internal users,
 * a GitHub OAuth app can be org-approved — and this page deliberately does not rebuild that policy.
 *
 * The page is a LIST now, not one form: a host stores several providers, each under an
 * operator-chosen key. Three states, exclusive by construction —
 *   - the list        the stored providers as cards, plus the callback URL;
 *   - the picker      "Add provider": a grid of preset tiles. A preset knows everything about a
 *                     provider except the two values only the operator has (client id + secret),
 *                     so picking one leaves a form that is mostly already filled in;
 *   - the form        one provider being added or edited.
 *
 * FOUR THINGS THE FORM MUST NOT GET WRONG:
 *
 *   1. The callback URL is READ-ONLY and comes from the server. It has to match what is registered
 *      on the provider's side byte for byte, and a value typed here (or derived from
 *      `location.origin`) would drift from the one the flow actually sends the moment the UI is
 *      reached by a second hostname. It is ONE URL for every provider.
 *   2. The client secret is write-only. A read answers `secretSet` and nothing else; on an
 *      existing provider an EMPTY field means "keep what is stored" (the server's rule), so an
 *      operator editing the label cannot silently wipe it.
 *   3. A template preset's issuer (`https://<your-domain>.okta.com/…`) is not a value — the
 *      placeholder renders as its own required field and the URL is assembled in view, because the
 *      server refuses the template saved verbatim and the operator should never meet that refusal.
 *   4. The role its accounts are created at is ON SCREEN, and its resting state is INHERIT. This
 *      field existed on the wire from the start and was never drawn, so the form sent whatever
 *      `blank()` happened to hold — which was `admin`, explicitly, for every provider anyone added
 *      here. Inherit is the empty string, resolved against the host's `default_role` when an
 *      account is provisioned rather than frozen at save time; it can only ever resolve to viewer
 *      on a host that set nothing, never to admin by omission.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import { ROLES, type ClaimMap, type HostSettings, type Role, type SsoConfig, type SsoConfigResponse, type SsoPreset, type SsoProviderEntry } from '../api/types';
import { sentence } from '../composables/useFormat';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import RelativeTime from '../components/RelativeTime.vue';
import SelectMenu from '../components/SelectMenu.vue';
import SkeletonList from '../components/SkeletonList.vue';
import SsoMark from '../components/SsoMark.vue';

const OIDC_CLAIMS: ClaimMap = { subject: 'sub', username: 'preferred_username', email: 'email', name: 'name', avatar: 'picture' };

const blank = (): SsoConfig => ({
  mode: 'oidc',
  enabled: true,
  label: '',
  clientId: '',
  discoveryUrl: '',
  provider: '',
  authorizeUrl: '',
  tokenUrl: '',
  userInfoUrl: '',
  emailsUrl: '',
  groupsUrl: '',
  scopes: '',
  claimMap: { ...OIDC_CLAIMS },
  allowedEmailDomains: [],
  allowedUsernames: [],
  requiredGroups: [],
  // EMPTY = INHERIT the host's default role, resolved when the account is provisioned rather than
  // frozen into the provider now — so raising or lowering the host default moves every inheriting
  // provider with it. It resolves to viewer on a host where nobody set one, and never to admin.
  //
  // THE HISTORY, because losing it is how this comes back: this line said 'admin', and since the
  // form never rendered the field, every provider added here sent `defaultRole: "admin"`
  // EXPLICITLY — which the server honours. Anyone who completed the OAuth flow against such a
  // provider became a full administrator, and the viewer default the server grew in 0.32.0 was
  // defeated through the most common path there is. It then said 'viewer', which was safe but still
  // a value chosen by a form nobody could see.
  //
  // Now the field is on screen with this as its resting state, so granting the most privilege this
  // product has is something a person picked, in words.
  defaultRole: '',
});

/**
 * The select's stand-in for "inherit". It cannot be `''`: reka's `SelectItem` throws on an
 * empty-string value (that is how it clears a selection and shows the placeholder), so the empty
 * string the API wants is mapped at the binding rather than stored in the form.
 */
const INHERIT = 'inherit';

/** One line each, in the picker itself — a role name alone does not say what it costs to grant. */
const ROLE_WHAT: Record<Role, string> = {
  viewer: 'reads everything',
  developer: 'deploys and tears down',
  maintainer: 'host configuration',
  admin: 'accounts and sign-on',
};

/**
 * The host's own default, read only so "inherit" can say what it currently resolves to. Best
 * effort: an older server has no such route, and the option still reads correctly without it.
 */
const hostDefaultRole = ref('');
void api.get<HostSettings>('/api/settings').then((r) => {
  if (r.ok) hostDefaultRole.value = String(r.body.settings.find((s) => s.key === 'default_role')?.value ?? '');
});

const roleOptions = computed(() => [
  {
    value: INHERIT,
    label: 'The host default',
    hint: hostDefaultRole.value ? `${sentence(hostDefaultRole.value)} today — follows Settings` : 'set in Settings',
  },
  ...ROLES.map((r) => ({ value: r, label: sentence(r), hint: ROLE_WHAT[r] })),
]);

const providers = ref<SsoProviderEntry[]>([]);
const presets = ref<SsoPreset[]>([]);
const callbackUrl = ref('');
const loaded = ref(false);
const listError = ref('');

type Editing = {
  /** null while adding; otherwise the stored key being edited. The key is the row's identity. */
  originalKey: string | null;
  key: string;
  /** The preset the config came from, for the setup link and the template flow. */
  preset: SsoPreset | null;
  form: SsoConfig;
  secret: string;
  secretSet: boolean;
  // The three rule lists are edited as comma-separated text, split on save. A `string[]` bound to
  // an input cannot hold "acme," while someone is still typing the second entry.
  domains: string;
  usernames: string;
  groups: string;
  /** One value per `<placeholder>` in a template preset's issuer, keyed by placeholder name. */
  placeholders: Record<string, string>;
};
const editing = ref<Editing | null>(null);

/** `''` on the wire, the sentinel in the control. Nothing else in the form needs translating. */
const roleChoice = computed<string>({
  get: () => editing.value?.form.defaultRole || INHERIT,
  set: (v) => {
    if (editing.value) editing.value.form.defaultRole = v === INHERIT ? '' : v;
  },
});

const picking = ref(false);
const saving = ref(false);
const formError = ref('');

async function load(): Promise<void> {
  const r = await api.get<SsoConfigResponse>('/api/sso/config');
  loaded.value = true;
  if (!r.ok) {
    listError.value = problem(r, 'read the sign-on settings');
    return;
  }
  listError.value = '';
  providers.value = r.body.providers ?? [];
  presets.value = r.body.presets ?? [];
  callbackUrl.value = r.body.callbackUrl;
}
void load();

/** With nothing configured the picker IS the page — an empty list with a button would be a detour. */
const showPicker = computed(() => !editing.value && (picking.value || (loaded.value && !listError.value && providers.value.length === 0)));

const pickerTiles = computed(() => [
  ...presets.value.map((p) => ({ preset: p as SsoPreset | null, id: p.key, label: p.label, mode: p.mode })),
  { preset: null, id: 'custom-oauth2', label: 'Custom OAuth 2.0', mode: 'oauth2' as const },
  { preset: null, id: 'custom-oidc', label: 'Custom OpenID Connect', mode: 'oidc' as const },
]);

/** A free slug for a second provider on the same preset: github, github-2, github-3… */
function suggestKey(base: string): string {
  const taken = new Set(providers.value.map((p) => p.key));
  if (!taken.has(base)) return base;
  for (let n = 2; ; n++) if (!taken.has(`${base}-${n}`)) return `${base}-${n}`;
}

function startAdd(tile: { preset: SsoPreset | null; mode: 'oidc' | 'oauth2' }): void {
  const p = tile.preset;
  const f = blank();
  f.mode = tile.mode;
  f.provider = p ? p.key : tile.mode === 'oauth2' ? 'custom' : '';
  if (p) {
    f.label = p.label;
    f.scopes = p.scopes;
    f.claimMap = { ...OIDC_CLAIMS, ...p.claimMap };
    if (p.mode === 'oauth2') {
      f.authorizeUrl = p.authorizeUrl;
      f.tokenUrl = p.tokenUrl;
      f.userInfoUrl = p.userInfoUrl;
      // emailsUrl/groupsUrl left blank on purpose: the server fills the preset's own back in, and
      // only while userInfoUrl is still the preset's — a self-hosted host's token must not be sent
      // to the public endpoints.
    } else if (!p.discoveryUrl.includes('<')) {
      f.discoveryUrl = p.discoveryUrl;
    }
  }
  editing.value = {
    originalKey: null,
    key: suggestKey(p ? p.key : tile.mode === 'oauth2' ? 'custom' : 'oidc'),
    preset: p,
    form: f,
    secret: '',
    secretSet: false,
    domains: '',
    usernames: '',
    groups: '',
    placeholders: {},
  };
  formError.value = '';
  picking.value = false;
}

function startEdit(entry: SsoProviderEntry): void {
  editing.value = {
    originalKey: entry.key,
    key: entry.key,
    preset: presets.value.find((x) => x.key === entry.config.provider) ?? null,
    form: { ...blank(), ...entry.config, claimMap: { ...OIDC_CLAIMS, ...entry.config.claimMap } },
    secret: '',
    secretSet: entry.secretSet,
    domains: (entry.config.allowedEmailDomains ?? []).join(', '),
    usernames: (entry.config.allowedUsernames ?? []).join(', '),
    groups: (entry.config.requiredGroups ?? []).join(', '),
    placeholders: {},
  };
  formError.value = '';
  picking.value = false;
}

/**
 * The template flow, add-mode only: an existing row's issuer is already concrete, whatever preset
 * it came from. Splits `https://<your-domain>.okta.com/…` into literals and placeholder names.
 */
const templateParts = computed<Array<string | { ph: string }> | null>(() => {
  const e = editing.value;
  if (!e || e.originalKey !== null || !e.preset || e.form.mode !== 'oidc') return null;
  const t = e.preset.discoveryUrl;
  if (!t.includes('<')) return null;
  const parts: Array<string | { ph: string }> = [];
  const re = /<([^>]+)>/g;
  let last = 0;
  for (let m = re.exec(t); m; m = re.exec(t)) {
    if (m.index > last) parts.push(t.slice(last, m.index));
    parts.push({ ph: m[1] ?? '' });
    last = m.index + m[0].length;
  }
  if (last < t.length) parts.push(t.slice(last));
  return parts;
});

const placeholderNames = computed(() =>
  (templateParts.value ?? []).flatMap((p) => (typeof p === 'string' ? [] : [p.ph])),
);

/** The issuer as assembled so far. An unfilled placeholder stays visible as `<name>`. */
const assembledDiscovery = computed(() => {
  const parts = templateParts.value;
  const e = editing.value;
  if (!parts || !e) return '';
  return parts.map((p) => (typeof p === 'string' ? p : e.placeholders[p.ph]?.trim() || `<${p.ph}>`)).join('');
});

const keyRe = /^[a-z0-9][a-z0-9-]{0,31}$/;

/** Why the save button is disabled — shown as its title, per the disabled-controls rule. */
const cannotSave = computed<string>(() => {
  const e = editing.value;
  if (!e) return 'nothing is being edited';
  const key = e.key.trim();
  if (!keyRe.test(key)) return 'the key must be a lowercase slug of letters, digits and dashes (32 at most)';
  if (e.originalKey === null && providers.value.some((p) => p.key === key))
    return `a provider is already stored under "${key}" — edit that one instead, or pick another key`;
  if (!e.form.clientId.trim()) return 'the client id is required';
  if (!e.secretSet && !e.secret.trim()) return 'the client secret is required';
  if (e.form.mode === 'oidc') {
    const url = templateParts.value ? assembledDiscovery.value : e.form.discoveryUrl.trim();
    if (!url) return 'the issuer is required';
    if (url.includes('<')) return 'fill in the issuer placeholder first';
  } else if (!e.form.authorizeUrl.trim() || !e.form.tokenUrl.trim()) {
    return 'the authorize and token URLs are required';
  }
  return '';
});

const list = (s: string): string[] => s.split(',').map((v) => v.trim()).filter(Boolean);

async function save(): Promise<void> {
  const e = editing.value;
  if (!e) return;
  saving.value = true;
  // Annotated, not inline: `api.put` takes `unknown`, so without a type here a mistyped field
  // (`allowedUsernams`) would compile, the server would see no such field, the rule would silently
  // restrict nobody, and the form would say "Saved".
  const body: SsoConfig & { key: string; clientSecret: string } = {
    ...e.form,
    key: e.key.trim(),
    clientSecret: e.secret.trim(),
    discoveryUrl: templateParts.value ? assembledDiscovery.value : e.form.discoveryUrl,
    allowedEmailDomains: list(e.domains),
    allowedUsernames: list(e.usernames),
    requiredGroups: e.form.mode === 'oidc' ? [] : list(e.groups),
  };
  const r = await api.put<{ ok: boolean; key: string }>('/api/sso/config', body);
  saving.value = false;
  if (!r.ok) {
    // Verbatim, right above the button that was pressed — the refusals are written to be read
    // (the scope refusal names the scope; the template refusal names the placeholder).
    formError.value = problem(r, 'save the provider');
    return;
  }
  formError.value = '';
  toast('ok', 'Saved.');
  editing.value = null;
  void load();
}

async function remove(entry: SsoProviderEntry): Promise<void> {
  const r = await api.del(`/api/sso/config/${entry.key}`);
  if (!r.ok) {
    listError.value = problem(r, 'remove the provider');
    return;
  }
  toast('ok', `Removed ${entry.key}. Its accounts keep working.`);
  void load();
}

async function copyCallback(): Promise<void> {
  try {
    await navigator.clipboard.writeText(callbackUrl.value);
    toast('ok', 'Copied.');
  } catch {
    toast('error', 'Copy failed.');
  }
}

/** The card's second line, first half: what kind of provider this row talks to. */
function whereLine(c: SsoConfig): string {
  if (c.mode === 'oidc') {
    try {
      return `OpenID Connect · ${new URL(c.discoveryUrl).hostname}`;
    } catch {
      return 'OpenID Connect';
    }
  }
  const p = presets.value.find((x) => x.key === c.provider);
  return p ? `OAuth 2.0 · ${p.label}` : 'OAuth 2.0 · custom endpoints';
}

/**
 * The card's third fact: what an account this provider mints is created as — visible in the list
 * because a provider that hands out admin should not need opening to find that out.
 */
function roleLine(c: SsoConfig): string {
  if (c.defaultRole) return `new accounts: ${sentence(c.defaultRole)}`;
  return `new accounts: the host default${hostDefaultRole.value ? ` (${sentence(hostDefaultRole.value)})` : ''}`;
}

/** The card's second line, second half: the sign-in rules, or the honest default. */
function rulesLine(c: SsoConfig): string {
  const parts: string[] = [];
  if (c.allowedEmailDomains?.length) parts.push(`domains ${c.allowedEmailDomains.join(', ')}`);
  if (c.allowedUsernames?.length) parts.push(`usernames ${c.allowedUsernames.join(', ')}`);
  if (c.requiredGroups?.length) parts.push(`groups ${c.requiredGroups.join(', ')}`);
  return parts.length ? `lets in ${parts.join('; ')}` : 'lets in anyone the provider allows';
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Single sign-on</h1>
        <div class="sub">Sign in with the identity providers you already run</div>
      </div>
      <span class="grow" />
      <button v-if="loaded && !listError && !editing && !showPicker" class="primary" @click="picking = true">Add provider</button>
    </div>

    <ErrorNote v-if="listError" :text="listError" title="Single sign-on." />
    <SkeletonList v-if="!loaded" :rows="4" />

    <!-- ============================ the form: one provider ============================ -->
    <template v-else-if="editing">
      <section class="panel">
        <div class="phead">
          <SsoMark :preset="editing.form.provider" :size="20" />
          <h2 class="section">
            {{
              editing.originalKey
                ? `Edit ${editing.originalKey}`
                : `Add ${editing.preset?.label ?? (editing.form.mode === 'oidc' ? 'an OpenID Connect issuer' : 'a custom OAuth 2.0 provider')}`
            }}
          </h2>
          <span class="mute">{{ editing.form.mode === 'oidc' ? 'OpenID Connect' : 'OAuth 2.0' }}</span>
          <span class="grow" />
          <label class="check"><input v-model="editing.form.enabled" type="checkbox" /> Enabled</label>
        </div>

        <!-- Where to create the app. The preset's own `setupHint` prose is deliberately not drawn. -->
        <div v-if="editing.preset" class="banner plain">
          <p>
            <a :href="editing.preset.setupUrl" target="_blank" rel="noopener">
              Create the app in {{ editing.preset.label }} →
            </a>
          </p>
        </div>

        <!-- THE thing every operator hunts for, so it lives inside the form, first. -->
        <div class="field">
          <label>Redirect URI</label>
          <div class="row sso-copy">
            <code class="sso-url">{{ callbackUrl }}</code>
            <button type="button" @click="copyCallback">Copy</button>
          </div>
          <p class="hint">Must match the provider's registered callback exactly.</p>
        </div>

        <div v-if="!editing.originalKey" class="field">
          <label for="sso-key">Key</label>
          <input id="sso-key" v-model="editing.key" type="text" spellcheck="false" class="sso-key" />
          <p class="hint">Lowercase letters, digits, dashes.</p>
        </div>

        <div class="field">
          <label for="sso-label">Button text</label>
          <input id="sso-label" v-model="editing.form.label" type="text" :placeholder="editing.preset?.label || 'Acme SSO'" />
          <p class="hint">“Continue with {{ editing.form.label || editing.preset?.label || '…' }}”</p>
        </div>

        <!-- ── OIDC: the issuer. A template preset assembles it from the placeholder(s). -->
        <template v-if="editing.form.mode === 'oidc'">
          <template v-if="templateParts">
            <div v-for="ph in placeholderNames" :key="ph" class="field">
              <label :for="`ph-${ph}`">{{ sentence(ph) }}</label>
              <input :id="`ph-${ph}`" v-model="editing.placeholders[ph]" type="text" spellcheck="false" class="sso-key" />
            </div>
            <div class="field">
              <label>Issuer</label>
              <code class="sso-url" :data-incomplete="assembledDiscovery.includes('<') || undefined">{{ assembledDiscovery }}</code>
              <p class="hint">Fetched when you save.</p>
            </div>
          </template>
          <div v-else class="field">
            <label for="sso-issuer">Issuer</label>
            <input id="sso-issuer" v-model="editing.form.discoveryUrl" type="text" spellcheck="false" placeholder="https://accounts.google.com" />
            <p class="hint">Fetched when you save.</p>
          </div>
        </template>

        <div class="field">
          <label for="sso-client">Client id</label>
          <input id="sso-client" v-model="editing.form.clientId" type="text" spellcheck="false" autocomplete="off" />
        </div>
        <div class="field">
          <label for="sso-secret">Client secret</label>
          <input id="sso-secret" v-model="editing.secret" type="password" spellcheck="false" autocomplete="new-password" />
          <p class="hint">
            Write-only.
            {{ editing.secretSet ? 'Leave empty to keep the stored secret.' : 'Never returned once saved.' }}
          </p>
        </div>

        <div class="field">
          <label for="sso-scopes">Scopes</label>
          <input id="sso-scopes" v-model="editing.form.scopes" type="text" spellcheck="false" :placeholder="editing.form.mode === 'oidc' ? 'openid profile email' : ''" />
        </div>

        <!-- ── OAuth 2.0: the endpoints and the claim mapping. Filled in by the preset, needed
             open only for a custom provider or a self-hosted host — which is exactly the gitlab
             preset with its URLs replaced, so everything stays editable. -->
        <details v-if="editing.form.mode === 'oauth2'" class="sso-adv" :open="!editing.preset">
          <summary>Endpoints and claim mapping</summary>

          <div class="field">
            <label for="sso-auth">Authorize URL</label>
            <input id="sso-auth" v-model="editing.form.authorizeUrl" type="text" spellcheck="false" />
          </div>
          <div class="field">
            <label for="sso-token">Token URL</label>
            <input id="sso-token" v-model="editing.form.tokenUrl" type="text" spellcheck="false" />
          </div>
          <div class="field">
            <label for="sso-userinfo">User info URL</label>
            <input id="sso-userinfo" v-model="editing.form.userInfoUrl" type="text" spellcheck="false" />
          </div>
          <div class="field">
            <label for="sso-emails">Emails URL <span class="mute">(optional)</span></label>
            <input id="sso-emails" v-model="editing.form.emailsUrl" type="text" spellcheck="false" />
          </div>
          <div class="field">
            <label for="sso-groups-url">Groups URL <span class="mute">(optional)</span></label>
            <input id="sso-groups-url" v-model="editing.form.groupsUrl" type="text" spellcheck="false" />
          </div>

          <!-- Which key of the provider's user response holds each field; a preset fills these in. -->
          <h3 class="section" style="margin-top: var(--s4)">Claim mapping</h3>
          <div class="row" style="flex-wrap: wrap; gap: var(--s3)">
            <div v-for="k in (['subject', 'username', 'email', 'name', 'avatar'] as const)" :key="k" class="field inline">
              <label :for="`cm-${k}`">{{ k }}</label>
              <input :id="`cm-${k}`" v-model="editing.form.claimMap[k]" type="text" spellcheck="false" style="width: 11rem" />
            </div>
          </div>
        </details>
      </section>

      <!-- ============================ who gets in ============================ -->
      <section class="panel">
        <div class="phead"><h2 class="section">Who gets in</h2></div>
        <p class="dim">
          Every filled list must match; empty lists restrict nobody.
        </p>
        <div class="field">
          <label for="sso-domains">Allowed email domains</label>
          <input id="sso-domains" v-model="editing.domains" type="text" spellcheck="false" placeholder="example.com, corp.example" />
          <p class="hint">
            Comma-separated; subdomains count. <b>When set, a login with no address is refused.</b>
          </p>
        </div>

        <div class="field">
          <label for="sso-usernames">Allowed usernames</label>
          <input id="sso-usernames" v-model="editing.usernames" type="text" spellcheck="false" placeholder="octocat, qa-[0-9]*" />
          <p class="hint">
            Comma-separated patterns: <code>*</code>, <code>?</code>, <code>[0-9]</code>.
            <b>When set, a login with no username is refused.</b>
          </p>
        </div>

        <div v-if="editing.form.mode === 'oauth2'" class="field">
          <label for="sso-groups">Required groups</label>
          <input id="sso-groups" v-model="editing.groups" type="text" spellcheck="false" placeholder="acme, acme/backend" />
          <p class="hint">
            <b>Exact names, not patterns</b>, comma-separated. Needs the matching scope above, or
            the save is refused.
          </p>
        </div>

        <!-- Never rendered before today, while the form quietly sent `admin`. Every account this
             provider mints is created at this role, so it is the one field on the page that decides
             what a stranger who completes the OAuth flow can do. -->
        <div class="field">
          <label for="sso-role">New account role</label>
          <SelectMenu
            id="sso-role"
            v-model="roleChoice"
            label="Role for the accounts this provider creates"
            :options="roleOptions"
          />
          <p class="hint">
            Set at first sign-in. Host default follows
            <RouterLink to="/settings">Settings</RouterLink>.
          </p>
        </div>
      </section>

      <ErrorNote v-if="formError" :text="formError" title="Not saved." />

      <div class="row" style="gap: var(--s3)">
        <ActionButton variant="primary" :pending="saving" :disabled="saving || !!cannotSave" :title="cannotSave || undefined" @click="save">
          {{ editing.originalKey ? 'Save changes' : 'Add provider' }}
        </ActionButton>
        <button class="ghost" @click="editing = null">Cancel</button>
      </div>
    </template>

    <!-- ============================ the picker ============================ -->
    <template v-else-if="showPicker">
      <section class="panel">
        <div class="phead">
          <h2 class="section">{{ providers.length ? 'Add a provider' : 'Pick a provider' }}</h2>
          <span class="grow" />
          <button v-if="providers.length" class="ghost" @click="picking = false">Cancel</button>
        </div>
        <p class="dim">Pick where your team already signs in.</p>
        <div class="sso-tiles">
          <button v-for="t in pickerTiles" :key="t.id" class="sso-tile" @click="startAdd(t)">
            <SsoMark :preset="t.preset?.key ?? 'custom'" :size="22" />
            <span>{{ t.label }}</span>
            <span class="sso-tile-mode">{{ t.mode === 'oidc' ? 'OpenID Connect' : 'OAuth 2.0' }}</span>
          </button>
        </div>
      </section>
    </template>

    <!-- ============================ the list ============================ -->
    <template v-else-if="!listError">
      <section class="panel">
        <div class="phead"><h2 class="section">Providers</h2></div>
        <div class="sso-cards stagger">
          <div v-for="(p, i) in providers" :key="p.key" class="sso-card" :style="{ '--i': i }">
            <SsoMark :preset="p.config.provider" :size="22" />
            <div class="sso-card-main">
              <div class="sso-card-head">
                <b>{{ p.config.label }}</b>
                <code class="mute">{{ p.key }}</code>
                <span v-if="!p.config.enabled" class="badge off">disabled</span>
              </div>
              <div class="sso-card-sub">
                {{ whereLine(p.config) }} · {{ rulesLine(p.config) }} · {{ roleLine(p.config) }} ·
                updated <RelativeTime :at="p.updatedAt" />
              </div>
            </div>
            <div class="row-actions">
              <button class="ghost sm" @click="startEdit(p)">Edit</button>
              <ActionButton variant="ghost" class="sm sso-remove" :confirm="`Remove ${p.key}?`" @run="remove(p)">
                Remove
              </ActionButton>
            </div>
          </div>
        </div>
      </section>

      <section class="panel">
        <div class="phead"><h2 class="section">Callback URL</h2></div>
        <p class="dim">Register this with every provider.</p>
        <div class="row sso-copy">
          <code class="sso-url">{{ callbackUrl }}</code>
          <button type="button" @click="copyCallback">Copy</button>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
/* ── the picker tiles: brand mark, name, protocol. Grouped boxes inside a panel → r2. */
.sso-tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: var(--s3);
}
.sso-tile {
  flex-direction: column;
  justify-content: center;
  gap: var(--s2);
  padding: var(--s4) var(--s3);
  border-radius: var(--r2);
}
.sso-tile-mode {
  font-size: var(--t-xs);
  font-weight: 400;
  color: var(--fg-mute);
}

/* ── the provider cards. */
.sso-cards {
  display: flex;
  flex-direction: column;
  gap: var(--s3);
}
.sso-card {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s3);
  border: 1px solid var(--line);
  border-radius: var(--r2);
  padding: var(--s3) var(--s4);
}
.sso-card > svg {
  flex: none;
}
.sso-card-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: var(--s1);
}
.sso-card-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
}
.sso-card-sub {
  font-size: var(--t-sm);
  color: var(--fg-dim);
  overflow-wrap: anywhere;
}
/* Ghost so the row stays quiet, red so nobody mistakes what it does. */
.sso-remove {
  color: var(--fail);
}

/* ── the callback URL: the one element here allowed to scroll sideways, never to wrap. */
.sso-copy {
  align-items: center;
  gap: var(--s3);
}
.sso-url {
  flex: 1;
  min-width: 0;
  overflow-x: auto;
  white-space: nowrap;
}
/* Block display when it sits alone in a field (the assembled issuer). */
.field > .sso-url {
  display: block;
  flex: none;
}
.sso-url[data-incomplete] {
  color: var(--fg-mute);
}

/* Slugs and placeholders are short — a 560px input for "acme" reads as a text editor. */
.sso-key {
  max-width: 260px;
}

/* ── the collapsed endpoint block for a preset. */
.sso-adv > summary {
  cursor: pointer;
  font-size: var(--t-sm);
  color: var(--fg-dim);
  margin: var(--s4) 0 var(--s2);
}
</style>
