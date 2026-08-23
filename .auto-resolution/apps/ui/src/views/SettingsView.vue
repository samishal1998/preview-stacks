<script setup lang="ts">
/**
 * Settings: where the API is, how to authenticate to it, and how the app looks.
 *
 * Everything here is client-side and persisted to localStorage by `useSettings` — nothing is sent
 * to the server, and there is nothing to save. Bindings write through immediately.
 */
import { computed, ref } from 'vue';
import { settings } from '../composables/useSettings';
import { state, loadHealth } from '../composables/useControlPlane';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import InfoHint from '../components/InfoHint.vue';
import SelectMenu from '../components/SelectMenu.vue';

const checking = ref(false);
const reveal = ref(false);

/**
 * What is in the token box, described without printing it.
 *
 * The point is to make a WRONG value visible at a glance. A password autofilled into this field is
 * short and matches neither shape, and "12 characters, not a pstack token" is the sentence that
 * ends the mystery — before this, the only symptom was every page 401ing.
 */
const tokenShape = computed(() => {
  const t = settings.token;
  if (t.startsWith('pstack_pat_')) return 'Looks like a personal token.';
  if (/^[0-9a-f]{40,}$/i.test(t)) return 'Looks like a machine token.';
  return `${t.length} characters — this does not look like a pstack token.`;
});

/**
 * The server tells us whether it enforces auth. When it does not, it is bound to loopback and a
 * token would be pointless — worth saying, so nobody hunts for one that was never issued.
 */
const authEnforced = computed(() => state.health?.authEnforced ?? null);

async function recheck(): Promise<void> {
  checking.value = true;
  try {
    await loadHealth();
    toast(state.healthError ? 'error' : 'ok', state.healthError || 'Control plane reachable');
  } finally {
    checking.value = false;
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Settings</h1>
        <div class="sub">Stored in this browser only. Nothing here is sent to the server.</div>
      </div>
    </div>

    <!-- `settings-form` caps the field widths: a URL input stretched to the panel's full 1500px
         reads as a text editor, not a form — see the rule in app.css. -->
    <section class="panel settings-form">
      <h2 class="phead-title">Connection</h2>

      <div class="field">
        <label for="apiBase">API base URL</label>
        <input
          id="apiBase"
          v-model.trim="settings.apiBase"
          type="url"
          spellcheck="false"
          placeholder="(same origin)"
        />
        <div class="mute hint">
          Leave it empty unless you are pointing this page at another host.
          <InfoHint label="about the API base URL">
            Empty means same-origin, which is how the control stack serves this page — the browser
            never makes a cross-origin request, so there is no CORS to configure. Set an absolute
            URL only for a development session against a remote host.
          </InfoHint>
        </div>
      </div>

      <div class="field">
        <label for="token">Access token</label>
        <!--
          NOT `type="password"`, and none of these attributes are decorative.

          This field is why "opening Settings signs me out" was a real bug. A password input is an
          autofill magnet: Chrome and every password manager fill one on sight and ignore
          `autocomplete="off"` while doing it. The filled value went straight through `v-model` into
          localStorage and onto every request as `Authorization: Bearer <someone's password>` — and
          the server used to treat a bad bearer as a hard refusal rather than falling back to the
          session cookie, so a perfectly good login 401'd on every route until site data was cleared.

          The server now falls through (that is the real fix), and this field stops inviting the
          fill: a text input that masks itself only while it holds a value, named nothing like a
          password, with the ignore hints the managers respect.
        -->
        <input
          id="token"
          v-model.trim="settings.token"
          :type="reveal || !settings.token ? 'text' : 'password'"
          name="pstack-access-token"
          autocomplete="off"
          data-1p-ignore
          data-lpignore="true"
          data-bwignore
          data-form-type="other"
          spellcheck="false"
          placeholder="paste the token from pstack init"
        />
        <div class="row" style="margin-top: var(--s2)">
          <button v-if="settings.token" class="ghost sm" @click="reveal = !reveal">
            {{ reveal ? 'Hide' : 'Show' }}
          </button>
          <ActionButton
            v-if="settings.token"
            variant="ghost"
            class="sm"
            confirm="Clear it?"
            @run="settings.token = ''"
          >
            Clear
          </ActionButton>
          <span v-if="settings.token" class="mute" style="font-size: var(--t-xs)">
            {{ tokenShape }}
          </span>
        </div>
        <div class="mute hint">
          Authenticates every request — signing in with an account does the same job, so set this
          only for token-based access (the machine token, or a personal token from your account).
          <template v-if="authEnforced === false">
            This server is not asking for one — it only accepts connections from this machine, so a
            token here is harmless but unnecessary.
          </template>
        </div>
      </div>

      <div class="row" style="gap: 8px; margin-top: 4px">
        <button class="btn" :disabled="checking" @click="recheck">
          {{ checking ? 'Checking…' : 'Test connection' }}
        </button>
        <span v-if="state.health" class="mute">
          Connected to pstack {{ state.health.version }}
          <InfoHint label="where data is stored">
            Deployment records and stored specs live in
            <code>{{ state.health.dataDir }}</code> on the host.
          </InfoHint>
        </span>
        <span v-else-if="state.healthError" class="bad">{{ state.healthError }}</span>
      </div>
    </section>

    <section class="panel">
      <h2 class="phead-title">Appearance</h2>

      <div class="field">
        <label for="theme">Theme</label>
        <SelectMenu
            v-model="settings.theme" id="theme"
            label="Theme"
            :options="[
              { value: 'system', label: 'Follow the system' },
              { value: 'dark', label: 'Dark' },
              { value: 'light', label: 'Light' },
            ]"
          />
      </div>

      <div class="field">
        <label for="motion">Motion</label>
        <SelectMenu
            v-model="settings.motion" id="motion"
            label="Motion"
            :options="[
              { value: 'system', label: 'Follow the system' },
              { value: 'full', label: 'Always animate' },
              { value: 'none', label: 'No animation' },
            ]"
          />
        <div class="mute hint">
          “Follow the system” uses your device's reduce-motion setting. The other two override it,
          in either direction.
        </div>
      </div>
    </section>
  </div>
</template>
