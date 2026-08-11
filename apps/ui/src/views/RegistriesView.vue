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
import InfoHint from '../components/InfoHint.vue';
import SkeletonList from '../components/SkeletonList.vue';
import HelpModal from '../components/HelpModal.vue';
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
  const stored = r.body.registry;
  if (stored && stored !== host.value) {
    // Docker Hub's canonical key differs from what anyone types; saying so avoids "I stored it and it
    // still is not used".
    toast('ok', `Stored for ${stored} — Docker Hub's canonical key.`);
  } else {
    toast('ok', `Stored for ${stored || host.value}. It applies to the next pull.`);
  }
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
        <div class="sub">
          Credentials for pulling private images
          <InfoHint label="why a host login is not enough">
            A pull is authenticated by the docker <em>client</em>, not the daemon: it reads its own
            <code>config.json</code> and hands the credential over. pstack's client runs inside the
            control container, so a <code>docker login</code> on the host writes a file it cannot see —
            and the pull fails on a host that is logged in.
          </InfoHint>
        </div>
      </div>
      <span class="grow" />
      <RefreshButton :run="load" />
    </div>

    <ErrorNote v-if="listError" :text="listError" title="Could not load the registry credentials." />

    <div v-if="writable === false" class="banner failed">
      <b>Credentials cannot be saved here yet.</b>
      <p>
        Re-run setup on the host — it is safe to repeat.
        <HelpModal title="Why this happens">
          <p>
            Saving a credential needs a writable directory that the control stack only started
            providing in version 0.7.0. A host set up before then does not have it, so this page can
            list credentials but not add one.
          </p>
          <p>
            Re-running setup is safe to repeat and recreates the control containers with the
            directory in place. Nothing already stored is lost.
          </p>
        </HelpModal>
      </p>
    </div>

    <!--
      The trap worth naming: a config.json copied from a laptop usually has credsStore and NO auths,
      because the secrets are in the OS keychain. Inside the container that helper does not exist, so
      every pull fails and an empty list looks like the reason.
    -->
    <div v-if="helpers.length" class="banner warn">
      <b>This file uses a credential helper, which will not work here.</b>
      <p>
        Found: <span class="mono">{{ helpers.join(', ') }}</span>. A helper keeps the secret outside the
        file — in an OS keychain, usually — and the helper binary does not exist in this container, so
        pulls from private registries will fail. Add the credential below instead, which
        stores it in the file.
      </p>
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
      <p v-else class="mute">
        Nothing stored. Public images need no credential — this is only for a private registry.
      </p>
    </section>

    <section class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Add a credential</h2>

      <p class="dim">
        Applies to the <b>next pull</b> — nothing needs restarting.
        <InfoHint label="why no restart is needed">
          The docker client re-reads <code>config.json</code> on every invocation, so there is no cache
          to bust. Storing one now is enough for a deploy started a second later.
        </InfoHint>
      </p>

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
        <div class="mute hint">
          Docker Hub is stored under its canonical key.
        </div>
      </div>

      <div class="field" style="max-width: 380px">
        <label for="user">Username</label>
        <input id="user" v-model.trim="username" type="text" spellcheck="false" autocomplete="off" />
      </div>

      <div class="field" style="max-width: 380px">
        <label for="pass">Password or token</label>
        <input id="pass" v-model="password" type="password" autocomplete="off" spellcheck="false" />
        <div class="mute hint">
          Prefer a token scoped to reading packages. Stored on the host as reversible base64, exactly as
          Stored the same way a terminal login stores it — never sent back to this page.
        </div>
      </div>

      <ErrorNote v-if="formError" :text="formError" title="Could not store this credential." />

      <div class="row" style="margin-top: var(--s4)">
        <ActionButton variant="primary" :pending="saving" :disabled="!canSave" @click="save">
          {{ saving ? 'Storing…' : 'Store' }}
        </ActionButton>
        <span v-if="!settings.token" class="mute">Storing needs an access token.</span>
      </div>

      <p class="hint">
        Or from the host, which writes the same file:
        <span class="mono">docker login --config {{ dir || '<DATA_DIR>/control/docker' }} &lt;registry&gt;</span>
      </p>
    </section>
  </div>
</template>
