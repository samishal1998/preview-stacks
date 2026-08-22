<script setup lang="ts">
/**
 * Single sign-on: point this control plane at the identity provider the operator already runs.
 *
 * THE POINT OF THE PAGE is that nobody has to create accounts here any more. Whoever can
 * authenticate against the operator's directory can sign in, and the account appears on first
 * login. The provider keeps owning who is allowed — Google Workspace restricts to internal users,
 * a GitHub OAuth app can be org-approved — and this page deliberately does not rebuild that policy.
 *
 * TWO THINGS THIS FORM MUST NOT GET WRONG:
 *
 *   1. The callback URL is READ-ONLY and comes from the server. It has to match what is registered
 *      on the provider's side byte for byte, and a value typed here (or derived from
 *      `location.origin`) would drift from the one the flow actually sends the moment the UI is
 *      reached by a second hostname.
 *   2. The client secret is write-only. The server answers with a mask; submitting the mask back
 *      unchanged keeps the stored secret, so an operator editing the label cannot silently wipe it.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import type { ClaimMap, SsoConfig, SsoConfigResponse, SsoPreset } from '../api/types';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import HelpModal from '../components/HelpModal.vue';
import InfoHint from '../components/InfoHint.vue';
import SelectMenu from '../components/SelectMenu.vue';
import SkeletonList from '../components/SkeletonList.vue';

const OIDC_CLAIMS: ClaimMap = { subject: 'sub', username: 'preferred_username', email: 'email', name: 'name', avatar: 'picture' };

const blank = (): SsoConfig => ({
  mode: 'oidc',
  enabled: true,
  label: '',
  clientId: '',
  discoveryUrl: '',
  provider: 'github',
  authorizeUrl: '',
  tokenUrl: '',
  userInfoUrl: '',
  emailsUrl: '',
  scopes: '',
  claimMap: { ...OIDC_CLAIMS },
  allowedEmailDomains: [],
  defaultRole: 'admin',
});

const form = ref<SsoConfig>(blank());
const secret = ref('');
const domains = ref('');
const presets = ref<SsoPreset[]>([]);
const callbackUrl = ref('');
const configured = ref(false);
const loaded = ref(false);
const error = ref('');
const saving = ref(false);

async function load(): Promise<void> {
  const r = await api.get<SsoConfigResponse>('/api/sso/config');
  loaded.value = true;
  if (!r.ok) {
    error.value = problem(r, 'read the sign-on settings');
    return;
  }
  error.value = '';
  presets.value = r.body.presets ?? [];
  callbackUrl.value = r.body.callbackUrl;
  configured.value = r.body.configured;
  if (r.body.config) {
    form.value = { ...blank(), ...r.body.config };
    domains.value = (r.body.config.allowedEmailDomains ?? []).join(', ');
  }
  // The mask, so leaving the field alone re-submits it and the server keeps what it has.
  secret.value = r.body.clientSecret;
}
void load();

/** Picking a preset fills the endpoints in. Everything stays editable — a self-hosted GitLab is
 *  this preset with three URLs replaced, not a `custom` provider with five fields typed by hand. */
function applyPreset(key: string): void {
  form.value.provider = key;
  const p = presets.value.find((x) => x.key === key);
  if (!p) {
    form.value.authorizeUrl = '';
    form.value.tokenUrl = '';
    form.value.userInfoUrl = '';
    form.value.emailsUrl = '';
    form.value.claimMap = { ...OIDC_CLAIMS };
    return;
  }
  form.value.authorizeUrl = p.authorizeUrl;
  form.value.tokenUrl = p.tokenUrl;
  form.value.userInfoUrl = p.userInfoUrl ?? '';
  // Left blank on purpose: the server fills the preset's own emails endpoint back in, and only
  // while userInfoUrl is still the preset's.
  form.value.emailsUrl = '';
  form.value.scopes = p.scopes;
  form.value.claimMap = { ...p.claimMap };
  if (!form.value.label || presets.value.some((x) => x.label === form.value.label)) form.value.label = p.label;
}

async function save(): Promise<void> {
  saving.value = true;
  const r = await api.put<{ ok: boolean; callbackUrl: string }>('/api/sso/config', {
    ...form.value,
    clientSecret: secret.value,
    allowedEmailDomains: domains.value.split(',').map((d) => d.trim()).filter(Boolean),
  });
  saving.value = false;
  if (!r.ok) {
    error.value = problem(r, 'save the provider');
    return;
  }
  error.value = '';
  toast('ok', 'Saved. The provider answered, so the settings are known good.');
  void load();
}

async function remove(): Promise<void> {
  const r = await api.del('/api/sso/config');
  if (!r.ok) {
    error.value = problem(r, 'remove the provider');
    return;
  }
  toast('ok', 'Removed. Existing accounts keep working.');
  form.value = blank();
  secret.value = '';
  domains.value = '';
  configured.value = false;
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

const canSave = computed(() => {
  if (!form.value.clientId.trim() || !secret.value) return false;
  if (form.value.mode === 'oidc') return !!form.value.discoveryUrl.trim();
  return !!form.value.authorizeUrl.trim() && !!form.value.tokenUrl.trim();
});
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
              would just be a second list to keep in step. Narrow it with the allowed email domains
              below if you need to.
            </p>
            <p>
              <b>Identity is the provider's subject, not the email.</b> Someone changing their
              address keeps their account, their history and their tokens. An address is only used to
              adopt an account that already exists here, and only when the provider says it is
              verified.
            </p>
            <p>
              <b>Nothing about local accounts changes.</b> The admin credential, manually created
              users and personal API tokens all keep working exactly as they do now.
            </p>
          </HelpModal>
        </h1>
        <div class="sub">
          Let people sign in with the identity provider you already run
          <InfoHint label="what is stored">
            One row: the endpoints, the client id, and the client secret. The secret has no read
            path — this page is shown a mask, and leaving it alone keeps what is stored.
          </InfoHint>
        </div>
      </div>
    </div>

    <ErrorNote v-if="error" :text="error" title="Single sign-on." />
    <SkeletonList v-if="!loaded" :rows="5" />

    <template v-else>
      <!-- ============================ the callback URL ============================ -->
      <section class="panel">
        <div class="phead"><h2 class="section">Register this callback URL</h2></div>
        <p class="dim">
          Paste this into your provider's application as the redirect / callback URL. It has to match
          exactly, and it is the same for every provider — a mismatch is the single most common
          reason the exchange fails, and the provider's error rarely says so.
        </p>
        <div class="row" style="gap: var(--s3); align-items: center">
          <code class="code" style="flex: 1; overflow-x: auto; white-space: nowrap">{{ callbackUrl }}</code>
          <button @click="copyCallback">Copy</button>
        </div>
      </section>

      <!-- ============================ the provider ============================ -->
      <section class="panel">
        <div class="phead">
          <h2 class="section">Provider</h2>
          <span class="grow" />
          <label class="check"><input v-model="form.enabled" type="checkbox" /> Enabled</label>
        </div>

        <div class="row" style="gap: var(--s4); flex-wrap: wrap">
          <div class="field inline">
            <label for="sso-mode">Kind</label>
            <SelectMenu
              id="sso-mode"
              :model-value="form.mode"
              label="Which protocol"
              :options="[
                { value: 'oidc', label: 'OpenID Connect' },
                { value: 'oauth2', label: 'OAuth 2.0' },
              ]"
              @update:model-value="(v) => (form.mode = v as 'oidc' | 'oauth2')"
            />
          </div>
          <div v-if="form.mode === 'oauth2'" class="field inline">
            <label for="sso-preset">Provider</label>
            <SelectMenu
              id="sso-preset"
              :model-value="form.provider"
              label="Which provider"
              :options="[...presets.map((p) => ({ value: p.key, label: p.label })), { value: 'custom', label: 'Custom' }]"
              @update:model-value="(v) => applyPreset(String(v))"
            />
          </div>
          <div class="field inline">
            <label for="sso-label">Button text</label>
            <input id="sso-label" v-model="form.label" type="text" placeholder="Acme SSO" />
          </div>
        </div>

        <p v-if="form.mode === 'oidc'" class="hint">
          Everything but the client id and secret is discovered from the issuer. GitHub is the one
          exception you may reach for and cannot use here: its OIDC document signs Actions job
          tokens, not user logins — use OAuth 2.0 for GitHub.
        </p>

        <div v-if="form.mode === 'oidc'" class="field">
          <label for="sso-issuer">Issuer</label>
          <input id="sso-issuer" v-model="form.discoveryUrl" type="text" spellcheck="false" placeholder="https://accounts.google.com" />
          <p class="hint">Or the full <code>…/.well-known/openid-configuration</code> URL. It is fetched when you save, so a typo is refused here rather than at someone's first login.</p>
        </div>

        <div class="field">
          <label for="sso-client">Client id</label>
          <input id="sso-client" v-model="form.clientId" type="text" spellcheck="false" autocomplete="off" />
        </div>
        <div class="field">
          <label for="sso-secret">Client secret</label>
          <input id="sso-secret" v-model="secret" type="password" spellcheck="false" autocomplete="new-password" />
          <p class="hint">
            Write-only. {{ configured ? 'A secret is stored — leave this as it is to keep it.' : 'Stored on save and never returned by any route.' }}
          </p>
        </div>

        <template v-if="form.mode === 'oauth2'">
          <div class="field">
            <label for="sso-auth">Authorize URL</label>
            <input id="sso-auth" v-model="form.authorizeUrl" type="text" spellcheck="false" />
          </div>
          <div class="field">
            <label for="sso-token">Token URL</label>
            <input id="sso-token" v-model="form.tokenUrl" type="text" spellcheck="false" />
          </div>
          <div class="field">
            <label for="sso-userinfo">User info URL</label>
            <input id="sso-userinfo" v-model="form.userInfoUrl" type="text" spellcheck="false" />
            <p class="hint">Leave empty to take the identity from the token response alone — a subject and nothing else.</p>
          </div>
          <div class="field">
            <label for="sso-emails">Emails URL <span class="mute">(optional)</span></label>
            <input id="sso-emails" v-model="form.emailsUrl" type="text" spellcheck="false" />
            <p class="hint">
              Only consulted when the profile carries no address — which is GitHub's default. Filled
              in automatically for a preset, and needed only if you pointed the user info URL at your
              own host.
            </p>
          </div>
        </template>

        <div class="field">
          <label for="sso-scopes">Scopes</label>
          <input id="sso-scopes" v-model="form.scopes" type="text" spellcheck="false" :placeholder="form.mode === 'oidc' ? 'openid profile email' : ''" />
        </div>

        <div v-if="form.mode === 'oauth2'">
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
              <input :id="`cm-${k}`" v-model="form.claimMap[k]" type="text" spellcheck="false" style="width: 11rem" />
            </div>
          </div>
        </div>
      </section>

      <!-- ============================ who gets in ============================ -->
      <section class="panel">
        <div class="phead"><h2 class="section">Who gets an account</h2></div>
        <p class="dim">
          By default, anyone your provider lets authenticate. It already owns that decision; this is
          only here for the case where the application you registered is broader than the people who
          should reach this control plane.
        </p>
        <div class="field">
          <label for="sso-domains">Allowed email domains</label>
          <input id="sso-domains" v-model="domains" type="text" spellcheck="false" placeholder="example.com, corp.example" />
          <p class="hint">
            Comma-separated; subdomains count. Empty means no restriction. <b>Non-empty means a login
            with no email address at all is refused too</b> — a provider that hides the address (a
            private GitHub profile with no <code>user:email</code> scope) cannot be checked, so it is
            not waved through.
          </p>
        </div>
      </section>

      <div class="row" style="gap: var(--s3)">
        <ActionButton variant="primary" :pending="saving" :disabled="saving || !canSave" @click="save">
          {{ configured ? 'Save changes' : 'Enable single sign-on' }}
        </ActionButton>
        <span class="grow" />
        <ActionButton v-if="configured" variant="danger" :confirm="`Remove ${form.label || 'the provider'}?`" @run="remove">
          Remove provider
        </ActionButton>
      </div>
      <p v-if="configured" class="mute" style="font-size: var(--t-sm); margin-top: var(--s3)">
        Removing the provider does not delete anyone. Those accounts keep their personal tokens, and
        pointing the same provider back at this host re-links them by subject.
      </p>
    </template>
  </div>
</template>
