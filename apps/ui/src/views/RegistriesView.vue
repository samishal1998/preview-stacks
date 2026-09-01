<script setup lang="ts">
/**
 * Private registry credentials.
 *
 * WHY THIS PAGE EXISTS. An image pull is authenticated by the **client**, not the daemon: `docker pull`
 * reads its own `config.json` and hands the credential to the daemon. pstack shells out to compose from
 * inside the control container, so a `docker login` on the host writes a file that client cannot see —
 * and a private image fails with `pull access denied` on a host that is demonstrably logged in.
 *
 * WRITE-ONLY, DELIBERATELY. A `config.json` entry is `base64("user:password")` — reversible, not
 * encrypted. So there is no read path for it anywhere in the API, and this page shows hostnames and
 * usernames only. No reveal control, because the plaintext was never sent here; the same reasoning as
 * the masked variables. Re-entering a credential replaces it.
 *
 * The credential applies to the **next pull** — the CLI re-reads the file on every invocation, so
 * nothing needs restarting and there is no cache to bust. That is the property that makes "add creds on
 * demand" work at all, and it is stated on the page because otherwise the natural assumption is that a
 * redeploy is needed.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import type { RegistriesResponse, RegistryEntry } from '../api/types';
import { settings } from '../composables/useSettings';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import SkeletonList from '../components/SkeletonList.vue';
import RefreshButton from '../components/RefreshButton.vue';

const entries = ref<RegistryEntry[]>([]);
const helpers = ref<string[]>([]);
const dir = ref('');
const writable = ref<boolean | null>(null);
const loaded = ref(false);
const listError = ref('');

const host = ref('');
const username = ref('');
const password = ref('');
const saving = ref(false);
const formError = ref('');

const canSave = computed(
  () => !!settings.token && !saving.value && !!host.value && !!username.value && !!password.value,
);

async function load(): Promise<void> {
  const r = await api.get<RegistriesResponse>('/api/registries');
  loaded.value = true;
  if (!r.ok) {
    listError.value = problem(r, 'load the registry credentials');
    return;
  }
  listError.value = '';
  entries.value = r.body.entries ?? [];
  helpers.value = r.body.helpers ?? [];
  dir.value = r.body.dir ?? '';
  writable.value = r.body.writable ?? false;
}

void load();

async function save(): Promise<void> {
  saving.value = true;
  formError.value = '';
  const r = await api.put<{ registry: string }>(
    `/api/registries/${encodeURIComponent(host.value)}`,
    { username: username.value, password: password.value },
  );
  saving.value = false;
  if (!r.ok) {
    formError.value = problem(r, 'store this credential');
    return;
  }
  // Cleared immediately: there is no reason for a password to stay in a form field after it is stored,
  // and the page cannot read it back to re-populate one.
  password.value = '';
  // The server's key, not the typed one: Docker Hub canonicalises, and the list below reloads with
  // whatever it stored — so the toast does not have to explain the difference.
  toast('ok', `Stored for ${r.body.registry || host.value}.`);
  host.value = '';
  username.value = '';
  void load();
}

async function forget(registry: string): Promise<void> {
  const r = await api.del(`/api/registries/${encodeURIComponent(registry)}`);
  if (!r.ok) {
    listError.value = problem(r, 'forget this credential');
    return;
  }
  toast('ok', `Forgot ${registry}.`);
  void load();
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Registries</h1>
        <div class="sub">Credentials for pulling private images</div>
      </div>
      <span class="grow" />
      <RefreshButton :run="load" />
    </div>

    <ErrorNote v-if="listError" :text="listError" title="Could not load the registry credentials." />

    <!-- Gates the form below: this host can list credentials but not store one. -->
    <div v-if="writable === false" class="banner failed">
      <b>Credentials cannot be saved here. Re-run setup on the host.</b>
    </div>

    <!--
      The trap worth naming: a config.json copied from a laptop usually has credsStore and NO auths,
      because the secrets are in the OS keychain. Inside the container that helper does not exist, so
      every pull fails and an empty list looks like the reason.
    -->
    <div v-if="helpers.length" class="banner warn">
      <b>
        Credential helper <span class="mono">{{ helpers.join(', ') }}</span> — private pulls will
        fail here. Add the credential below.
      </b>
    </div>

    <section class="panel">
      <div class="phead">
        <h2 class="section">Stored</h2>
        <span class="grow" />
        <span v-if="dir" class="mute break" style="font-size: var(--t-sm)"><span class="mute">{{ dir }}</span></span>
      </div>

      <SkeletonList v-if="!loaded" :rows="2" />
      <ul v-else-if="entries.length" class="kvlist">
        <li v-for="e in entries" :key="e.registry">
          <span class="k break" style="width: 220px"><b>{{ e.registry }}</b></span>
          <span class="v row">
            <span>{{ e.username ?? 'unknown user' }}</span>
            <span v-if="e.viaHelper" class="badge warn">via helper</span>
            <span class="grow" />
            <ActionButton :disabled="!settings.token" variant="danger" @click="forget(e.registry)">
              Forget
            </ActionButton>
          </span>
        </li>
      </ul>
      <p v-else class="mute">No credentials yet. Add one below.</p>
    </section>

    <section class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Add a credential</h2>

      <p class="dim">Applies to the next pull.</p>

      <div class="field" style="max-width: 380px">
        <label for="host">Registry</label>
        <input
          id="host"
          v-model.trim="host"
          type="text"
          placeholder="ghcr.io"
          spellcheck="false"
          autocomplete="off"
        />
      </div>

      <div class="field" style="max-width: 380px">
        <label for="user">Username</label>
        <input id="user" v-model.trim="username" type="text" spellcheck="false" autocomplete="off" />
      </div>

      <div class="field" style="max-width: 380px">
        <label for="pass">Password or token</label>
        <input id="pass" v-model="password" type="password" autocomplete="off" spellcheck="false" />
        <!-- The secret interlock: reversible base64 on the host, and no read path back here. -->
        <div class="mute hint">Stored write-only, as reversible base64 — never shown again.</div>
      </div>

      <ErrorNote v-if="formError" :text="formError" title="Could not store this credential." />

      <div class="row" style="margin-top: var(--s4)">
        <ActionButton variant="primary" :pending="saving" :disabled="!canSave" @click="save">
          {{ saving ? 'Storing…' : 'Store' }}
        </ActionButton>
        <span v-if="!settings.token" class="mute">Storing needs an access token.</span>
      </div>
    </section>
  </div>
</template>
