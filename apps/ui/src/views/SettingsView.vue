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
import InfoHint from '../components/InfoHint.vue';

const checking = ref(false);

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

    <section class="panel">
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
        <input
          id="token"
          v-model.trim="settings.token"
          type="password"
          autocomplete="off"
          spellcheck="false"
          placeholder="paste the token from pstack init"
        />
        <div class="mute hint">
          Needed to deploy, tear down or delete. Viewing works without one.
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
        <select id="theme" v-model="settings.theme">
          <option value="system">Follow the system</option>
          <option value="dark">Dark</option>
          <option value="light">Light</option>
        </select>
      </div>

      <div class="field">
        <label for="motion">Motion</label>
        <select id="motion" v-model="settings.motion">
          <option value="system">Follow the system</option>
          <option value="full">Full animation</option>
          <option value="none">Reduce motion</option>
        </select>
        <div class="mute hint">
          “Follow the system” uses your device's reduce-motion setting. The other two override it,
          in either direction.
        </div>
      </div>
    </section>
  </div>
</template>
