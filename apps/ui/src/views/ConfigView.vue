<script setup lang="ts">
/**
 * Apply a host configuration: a `pstack pull config` export, sealed, from another host.
 *
 * ── WHY THERE IS NO DOWNLOAD BUTTON ──────────────────────────────────────────────────────────────
 *
 * This page applies a config and cannot produce one. That is deliberate and it is not an oversight
 * to be tidied up later.
 *
 * Export is exfiltration: one request and every credential on the host — account hashes, API
 * tokens, host secrets, registry logins, notifier URLs — is in the caller's hands. If a browser
 * session could do that, then an XSS, a borrowed laptop or a stolen cookie could do it too, in one
 * click. So `GET /api/config` refuses a session and answers only to the PSTACK_TOKEN bearer, and
 * the way to take an export is `pstack pull config` on a machine that already holds that token.
 *
 * Import runs the other way. The caller must ALREADY possess the sealed file and its passphrase —
 * so this page reveals nothing its user could not read for themselves — and every secret in it is
 * about to be plaintext on this host anyway. That is also why the passphrase is sent to the server
 * here while `pull` keeps it local: there is nothing left for it to protect from this host.
 *
 * It is still the widest-reaching write in the API. It can create an administrator and choose where
 * this host pulls its images from. Hence Preview: nothing is written until the operator has seen,
 * in words, what the file will make this host trust.
 */
import { ref } from 'vue';
import { api, problem } from '../api/client';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';

type Preview = {
  preview: true;
  trusts: string[];
  users: number;
  tokens: number;
  vars: number;
  notifiers: number;
  registries: number;
  routing: number;
  specs: number;
  sso: boolean;
};
type Applied = { trusts: string[]; created: string[]; skipped: string[] };

const sealed = ref('');
const fileName = ref('');
const passphrase = ref('');
const busy = ref('');
const error = ref('');
const preview = ref<Preview | null>(null);
const applied = ref<Applied | null>(null);

/**
 * Reading the file in the BROWSER, never uploading it as a form part.
 *
 * It is one small JSON envelope, and this keeps the whole thing on one request whose body we can
 * describe exactly. Picking a different file clears any preview: a preview that outlived the file
 * it described would be the one way this page could lie about what is being applied.
 */
async function pick(e: Event): Promise<void> {
  const f = (e.target as HTMLInputElement).files?.[0];
  preview.value = null;
  applied.value = null;
  error.value = '';
  if (!f) {
    sealed.value = '';
    fileName.value = '';
    return;
  }
  fileName.value = f.name;
  sealed.value = await f.text();
}

function clearPreview(): void {
  preview.value = null;
  applied.value = null;
}

async function run(mode: 'preview' | 'apply'): Promise<void> {
  error.value = '';
  busy.value = mode;
  const r = await api.post<Preview | Applied>('/api/config/sealed', {
    sealed: sealed.value,
    passphrase: passphrase.value,
    preview: mode === 'preview',
  });
  busy.value = '';
  if (!r.ok) {
    error.value = problem(r, mode === 'preview' ? 'preview this config' : 'apply this config');
    return;
  }
  if (mode === 'preview') {
    preview.value = r.body as Preview;
    return;
  }
  const done = r.body as Applied;
  applied.value = done;
  preview.value = null;
  toast('ok', `Applied: ${done.created.length} created, ${done.skipped.length} already here.`);
}
</script>

<template>
  <div class="page">
    <header class="page-head">
      <h1>Apply a configuration</h1>
      <p class="dim">
        A sealed <code>pstack pull config</code> export from another host: accounts, API tokens,
        variables and secrets, notifiers, sign-on, registry logins, routing files and named specs.
        It <b>creates what is missing and never overwrites</b>, so applying one twice changes
        nothing the second time.
      </p>
    </header>

    <section class="card">
      <label class="field">
        <span>Sealed export</span>
        <input type="file" accept=".sealed,.yaml,.yml,.json,application/json" @change="pick" />
        <small class="dim" v-if="fileName">{{ fileName }} — {{ sealed.length }} bytes</small>
      </label>

      <label class="field">
        <span>Passphrase</span>
        <input
          v-model="passphrase"
          type="password"
          autocomplete="off"
          placeholder="the passphrase this file was sealed with"
          @input="clearPreview"
        />
        <small class="dim">
          Sent to this host so it can open the file. That is safe in a way exporting would not be:
          everything inside is about to be stored here in plain text regardless.
        </small>
      </label>

      <div class="row">
        <ActionButton
          label="Preview"
          :pending="busy === 'preview'"
          :disabled="!sealed || !passphrase || !!busy"
          @click="run('preview')"
        />
        <ActionButton
          label="Apply"
          variant="danger"
          :pending="busy === 'apply'"
          :disabled="!preview || !!busy"
          @click="run('apply')"
        />
        <small class="dim" v-if="!preview && !applied">Preview first — Apply stays disabled until you have seen what this file does.</small>
      </div>
    </section>

    <!--
      The grant, in words, before anything is written. `trusts` names accounts and their roles, API
      tokens, the sign-on provider and every registry and notifier URL — the things a file from
      somewhere else could use to take this host over.
    -->
    <section v-if="preview" class="card banner warn">
      <b>This file will make this host trust:</b>
      <ul>
        <li v-for="t in preview.trusts" :key="t">{{ t }}</li>
        <li v-if="!preview.trusts.length" class="dim">nothing it does not already trust.</li>
      </ul>
      <p class="dim">
        {{ preview.users }} accounts · {{ preview.tokens }} API tokens · {{ preview.vars }} variables ·
        {{ preview.notifiers }} notifiers · {{ preview.registries }} registries ·
        {{ preview.routing }} routing files · {{ preview.specs }} specs<template v-if="preview.sso"> · a sign-on provider</template>
      </p>
      <p>Nothing has been written yet.</p>
    </section>

    <section v-if="applied" class="card">
      <b>Applied.</b>
      <p class="dim" v-if="applied.created.length">Created:</p>
      <ul>
        <li v-for="c in applied.created" :key="c">{{ c }}</li>
      </ul>
      <p class="dim" v-if="applied.skipped.length">Already here, left alone:</p>
      <ul>
        <li v-for="k in applied.skipped" :key="k" class="dim">{{ k }}</li>
      </ul>
    </section>

    <ErrorNote v-if="error" :text="error" title="This configuration was not applied." />

    <p class="dim">
      To <em>take</em> an export, run <code>pstack pull config -o host.sealed</code> on a machine
      that holds this host's <code>PSTACK_TOKEN</code>. It is deliberately not possible from a
      browser session: one click would otherwise hand over every credential on the host.
    </p>
  </div>
</template>
