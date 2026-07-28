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
          Leave empty for same-origin, which is how this UI is served in the control stack — nginx
          proxies <code>/api/</code> to the pstack container, so the browser never makes a
          cross-origin request and no CORS configuration exists to get wrong. Set an absolute URL
          only for a dev session against a tunnelled host.
        </div>
      </div>

      <div class="field">
        <label for="token">Bearer token</label>
        <input
          id="token"
          v-model.trim="settings.token"
          type="password"
          autocomplete="off"
          spellcheck="false"
          placeholder="PSTACK_TOKEN"
        />
        <div class="mute hint">
          Attached to <strong>POST, PUT and DELETE</strong> only — the routes that change or destroy
          something. Reads are unauthenticated, so the dashboard works before you paste one.
          <template v-if="authEnforced === false">
            <br />
            This server reports <code>authEnforced: false</code>: it is bound to loopback and accepts
            mutations without a token. A token here is harmless but unnecessary.
          </template>
        </div>
      </div>

      <div class="row" style="gap: 8px; margin-top: 4px">
        <button class="btn" :disabled="checking" @click="recheck">
          {{ checking ? 'Checking…' : 'Test connection' }}
        </button>
        <span v-if="state.health" class="mute">
          pstack {{ state.health.version }} · registry {{ state.health.dataDir }}
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
          “Follow the system” honours <code>prefers-reduced-motion</code>. The explicit options
          override it in both directions, because an OS-level preference is not always the right
          answer for one app on one screen.
        </div>
      </div>
    </section>
  </div>
</template>
