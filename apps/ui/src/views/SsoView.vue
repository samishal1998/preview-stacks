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
import HelpModal from '../components/HelpModal.vue';
import InfoHint from '../components/InfoHint.vue';
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
  /** The preset the config came from, for the setup walkthrough and the template flow. */
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
  toast('ok', e.form.mode === 'oidc' ? 'Saved — the issuer answered, so the settings are known good.' : 'Saved.');
  editing.value = null;
  void load();
}

async function remove(entry: SsoProviderEntry): Promise<void> {
  const r = await api.del(`/api/sso/config/${entry.key}`);
  if (!r.ok) {
    listError.value = problem(r, 'remove the provider');
    return;
  }
  toast('ok', `Removed ${entry.key}. Existing accounts keep working.`);
  void load();
}

async function copyCallback(): Promise<void> {
  try {
    await navigator.clipboard.writeText(callbackUrl.value);
    toast('ok', 'Copied.');
  } catch {
    toast('error', 'Could not reach the clipboard — select the text and copy it.');
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
        <h1>
          Single sign-on
          <HelpModal title="How single sign-on works here">
            <p>
              <b>This service is the relying party and nothing more.</b> You register an OAuth or
              OIDC application in your own organisation and paste its client id and secret here. Your
              directory stays yours — no accounts are copied, and nothing is synchronised.
            </p>
            <p>
              <b>Anyone who authenticates gets an account</b>, created on their first sign-in. That is
              deliberate: your provider already decides who may authenticate (Workspace restricts to
              internal users, a GitHub app can be org-approved), and duplicating that policy here
              would just be a second list to keep in step. Narrow it with the sign-in rules on each
              provider if you need to.
            </p>
            <p>
              <b>Identity is the provider's subject, not the email.</b> Someone changing their
              address keeps their account, their history and their tokens. An address is only used to
              adopt an account that already exists here, and only when the provider says it is
              verified.
            </p>
            <p>
              <b>Several providers share the accounts.</b> Each shows up as its own button on the
              login page, and the same person arriving from the same directory is the same account —
              nothing about local accounts or personal tokens changes either way.
            </p>
          </HelpModal>
        </h1>
        <div class="sub">
          Let people sign in with the identity providers you already run
          <InfoHint label="what is stored">
            One row per provider: the endpoints, the client id, and the client secret. The secret has
            no read path — this page only learns whether one is stored, and leaving the field empty
            keeps it.
          </InfoHint>
        </div>
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

        <!-- The walkthrough: where to create the app, in the preset's own words. -->
        <div v-if="editing.preset" class="banner plain">
          <b>Register the app with {{ editing.preset.label }}</b>
          <p>{{ editing.preset.setupHint }}</p>
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
          <p class="hint">
            Paste this into the provider's application as the redirect / callback URL. It has to
            match byte for byte, and it is the same for every provider on this host — a mismatch is
            the single most common reason the exchange fails, and the provider's error rarely says
            so.
          </p>
        </div>

        <div v-if="!editing.originalKey" class="field">
          <label for="sso-key">Key</label>
          <input id="sso-key" v-model="editing.key" type="text" spellcheck="false" class="sso-key" />
          <p class="hint">
            This host's name for the provider — it appears in the sign-in URL and in exports, never
            to the person signing in. Lowercase letters, digits and dashes.
          </p>
        </div>

        <div class="field">
          <label for="sso-label">Button text</label>
          <input id="sso-label" v-model="editing.form.label" type="text" :placeholder="editing.preset?.label || 'Acme SSO'" />
          <p class="hint">The login page draws it as “Continue with {{ editing.form.label || editing.preset?.label || '…' }}”.</p>
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
              <p class="hint">
                Assembled from the value{{ placeholderNames.length > 1 ? 's' : '' }} above —
                {{ editing.preset?.label }} gives every account its own issuer, so there is no single
                URL a preset could carry. It is fetched when you save, so a typo is refused here
                rather than at someone's first login.
              </p>
            </div>
          </template>
          <div v-else class="field">
            <label for="sso-issuer">Issuer</label>
            <input id="sso-issuer" v-model="editing.form.discoveryUrl" type="text" spellcheck="false" placeholder="https://accounts.google.com" />
            <p class="hint">
              Or the full <code>…/.well-known/openid-configuration</code> URL. Everything but the
              client id and secret is discovered from it, and it is fetched when you save — so a typo
              is refused here rather than at someone's first login.
            </p>
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
            {{ editing.secretSet ? 'A secret is stored — leave this empty to keep it.' : 'Stored on save and never returned by any route.' }}
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
            <p class="hint">Leave empty to take the identity from the token response alone — a subject and nothing else.</p>
          </div>
          <div class="field">
            <label for="sso-emails">Emails URL <span class="mute">(optional)</span></label>
            <input id="sso-emails" v-model="editing.form.emailsUrl" type="text" spellcheck="false" />
            <p class="hint">
              Only consulted when the profile carries no address — which is GitHub's default. Filled
              in automatically for a preset, and needed only if you pointed the user info URL at your
              own host.
            </p>
          </div>
          <div class="field">
            <label for="sso-groups-url">Groups URL <span class="mute">(optional)</span></label>
            <input id="sso-groups-url" v-model="editing.form.groupsUrl" type="text" spellcheck="false" />
            <p class="hint">
              Where this host asks which groups someone is in. Only consulted when there is a group
              rule below. Filled in automatically for a preset — but only while the user info URL is
              still the preset's, because a self-hosted host's token must not be sent to the public
              one, so a self-hosted provider needs this typed.
            </p>
          </div>

          <h3 class="section" style="margin-top: var(--s4)">
            Claim mapping
            <InfoHint label="what this is">
              Which key of the provider's user response holds each field. Flat lookups, not
              expressions. A preset fills these in; you only touch them for a provider nobody has
              written a preset for.
            </InfoHint>
          </h3>
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
        <div class="phead"><h2 class="section">Who gets an account, and as what</h2></div>
        <p class="dim">
          By default, anyone this provider lets authenticate. It already owns that decision; this is
          only here for the case where the application you registered is broader than the people who
          should reach this control plane. <b>Any one entry in a list is enough to satisfy that
          list, and every list you fill in has to be satisfied</b> — fill all three in and a login
          needs a listed domain, a matching username, and one of the groups. A list left empty is not
          a rule at all: it lets everyone past that check, it does not lock everyone out.
        </p>
        <div class="field">
          <label for="sso-domains">Allowed email domains</label>
          <input id="sso-domains" v-model="editing.domains" type="text" spellcheck="false" placeholder="example.com, corp.example" />
          <p class="hint">
            Comma-separated; subdomains count. Empty means no restriction. <b>Non-empty means a login
            with no email address at all is refused too</b> — a provider that hides the address (a
            private GitHub profile with no <code>user:email</code> scope) cannot be checked, so it is
            not waved through.
          </p>
        </div>

        <div class="field">
          <label for="sso-usernames">Allowed usernames</label>
          <input id="sso-usernames" v-model="editing.usernames" type="text" spellcheck="false" placeholder="octocat, qa-[0-9]*" />
          <p class="hint">
            Comma-separated patterns, case-insensitive: <code>*</code>, <code>?</code> and
            <code>[0-9]</code> classes. Empty means no restriction. <b>Non-empty means a login whose
            provider sends no username is refused too</b> — so this is a rule for a provider that
            actually has usernames (GitHub's <code>login</code>, GitLab's <code>username</code>). On
            an OpenID Connect issuer that sends no <code>preferred_username</code> it refuses
            everyone rather than nobody, so set it only where you know the provider sends one.
          </p>
        </div>

        <div v-if="editing.form.mode === 'oauth2'" class="field">
          <label for="sso-groups">Required groups</label>
          <input id="sso-groups" v-model="editing.groups" type="text" spellcheck="false" placeholder="acme, acme/backend" />
          <p class="hint">
            Comma-separated, and <b>exact names, not patterns</b> — an organisation login, or a group
            path like <code>acme/backend</code>. Case-insensitive. Empty means no restriction.
            Non-empty makes every sign-in ask the provider for that person's groups, so it needs the
            OAuth scope that endpoint requires: <b>add it to Scopes above, or the save is refused</b>
            — and the refusal names the scope. It also needs a provider whose group list this host
            knows how to read, which the save checks by name. If that call does not answer, the
            sign-in is refused rather than allowed.
          </p>
        </div>
        <p v-else class="hint">
          A group rule needs a provider whose groups endpoint this host knows how to read — an OAuth
          2.0 preset (GitHub, GitLab), not a discovered OpenID Connect issuer — so there is none
          here.
        </p>

        <!-- Never rendered before today, while the form quietly sent `admin`. Every account this
             provider mints is created at this role, so it is the one field on the page that decides
             what a stranger who completes the OAuth flow can do. -->
        <div class="field">
          <label for="sso-role">Role for the accounts it creates</label>
          <SelectMenu
            id="sso-role"
            v-model="roleChoice"
            label="Role for the accounts this provider creates"
            :options="roleOptions"
          />
          <p class="hint">
            Applied when an account is created here — on someone's first sign-in — and never after:
            promoting or demoting a person later is done in Accounts, and this does not reach back
            and change them.
            <b>The host default follows Settings</b> rather than being copied in now, so raising or
            lowering it there moves this provider with it.
            {{ hostDefaultRole ? `It is ${hostDefaultRole} today.` : '' }}
            Picking a role here pins this provider to it instead.
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
        <p class="dim">
          Pick where your team already signs in. A preset knows the provider's endpoints and its
          quirks — the only things it cannot know are the client id and secret you get when you
          register the app, and the form that opens says exactly where to do that.
        </p>
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
        <p class="mute" style="font-size: var(--t-sm); margin-top: var(--s3)">
          Removing a provider does not delete anyone. Those accounts keep their personal tokens, and
          pointing the same provider back at this host re-links them by subject.
        </p>
      </section>

      <section class="panel">
        <div class="phead"><h2 class="section">Callback URL</h2></div>
        <p class="dim">
          The redirect URI to register with every provider — one URL for all of them, built by the
          server so it cannot drift from the one the sign-in flow actually sends.
        </p>
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
