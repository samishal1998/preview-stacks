<script setup lang="ts">
/**
 * Settings: where the API is, how to authenticate to it, how the app looks — and the two settings
 * that live on the HOST.
 *
 * ── TWO KINDS OF CONTROL ON ONE PAGE ─────────────────────────────────────────────────────────────
 *
 * Nobody should have to guess which is which, so each panel says so in its own subtitle rather than
 * the page making one claim at the top: "nothing here is sent to the server" was true of this page
 * for eight versions, and a sentence like that outlives its truth in people's memory.
 *
 *   - THIS BROWSER — Connection and Appearance. `useSettings` writes them straight to localStorage;
 *     nothing is sent anywhere, nobody else sees them, and there is nothing to save because the
 *     bindings write through immediately. That is why those panels have no Save button.
 *   - THIS HOST — the concurrency limit and the default role for new accounts. Stored in the
 *     server's database, in force for everybody, and saved EXPLICITLY: a number box that wrote
 *     through on every keystroke would set the cap to 1 on the way to typing 12.
 *
 * ── WHERE THE VALUE CAME FROM, ALWAYS ────────────────────────────────────────────────────────────
 *
 * Both host settings render their `source`, because "I changed it and it did not stick" is
 * otherwise unanswerable: a box saying 4 cannot tell a refused write from a stale page from an
 * environment variable overriding it. Precedence is database > environment > built-in default — so
 * `PSTACK_MAX_JOBS` is now the DEFAULT rather than the authority, and a value set here is not
 * silently reverted by the next restart. A save flips the row's source to "db", so the write's own
 * response is what the page re-renders from; keeping the old row and patching just the number would
 * leave the line still crediting the environment.
 *
 * ── LOWERING THE CAP CANCELS NOTHING ─────────────────────────────────────────────────────────────
 *
 * Said beside the field, where it stays true; the toast only says it saved. Jobs already running
 * the cap applies to the next dispatch. Someone typing 1 while four jobs run must not read the
 * confirmation as "three were just killed" — which is also why the running and waiting counts sit
 * next to the input rather than a page away in Jobs.
 *
 * ── A VIEWER READS BOTH AND WRITES NEITHER ───────────────────────────────────────────────────────
 *
 * Disabled, not hidden, with the reason in text under the control: a page that hides what your tier
 * cannot reach looks broken rather than restricted, and the reason cannot live only in a `title`
 * because a disabled `SelectMenu` has no element of its own to hang one on. The threshold comes
 * from the server's own permission table (`minRole`, per key — maintainer for the cap, admin for
 * the role) and is never re-derived here. An unrecognised name is read as ADMIN: `rank()` scores a
 * role it has never heard of as 0, so passing one through unguarded would enable the control for
 * everyone. Hiding is courtesy either way — the server's 403 is the enforcement, and it lands in
 * `hostError`.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import { ROLES, type HostSettings, type Role, type SettingKey, type SettingRow, type SettingWritten } from '../api/types';
import { can } from '../composables/useAuth';
import { capProblem } from '../composables/useJobQueue';
import { settings } from '../composables/useSettings';
import { state, loadHealth, loadJobs } from '../composables/useControlPlane';
import { sentence } from '../composables/useFormat';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
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
  if (t.startsWith('pstack_pat_')) return 'Personal token.';
  if (/^[0-9a-f]{40,}$/i.test(t)) return 'Machine token.';
  return `${t.length} characters — not a pstack token.`;
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

// ── the host's settings ──────────────────────────────────────────────────────────────────────────

const host = ref<HostSettings | null>(null);
const hostError = ref('');
const hostLoaded = ref(false);
/**
 * What the boxes hold, kept apart from the server's copy so "changed" is a comparison, not a flag.
 *
 * `string | number` is not laziness about the type: `v-model` on `<input type="number">` REPLACES
 * the string with a number as soon as the value parses, so this ref genuinely holds both — a string
 * when it is empty or freshly reset from the server, a number after a keystroke.
 */
const maxJobsDraft = ref<string | number>('');
const roleDraft = ref('viewer');
/** Which key is in flight — one at a time, so a save cannot be racing its own re-read. */
const savingKey = ref<SettingKey | null>(null);

const row = (key: SettingKey): SettingRow | null => host.value?.settings.find((s) => s.key === key) ?? null;
const maxJobs = computed(() => row('max_jobs'));
const defaultRole = computed(() => row('default_role'));

/** Both drafts back to what the server just said. Called after every read and every write. */
function resetDrafts(): void {
  maxJobsDraft.value = String(maxJobs.value?.value ?? '');
  roleDraft.value = String(defaultRole.value?.value ?? 'viewer');
}

async function loadHost(): Promise<void> {
  const r = await api.get<HostSettings>('/api/settings');
  hostLoaded.value = true;
  if (!r.ok) {
    // Includes the honest 404: a server older than these settings has no such route, and `problem`
    // says exactly that rather than inventing a reason.
    hostError.value = problem(r, 'read the host settings');
    return;
  }
  hostError.value = '';
  host.value = r.body;
  resetDrafts();
}
void loadHost();
// The shell polls the job list every seven seconds once signed in; this is only so the counts
// beside the cap are right on arrival rather than up to a poll later.
void loadJobs();

async function save(key: SettingKey, value: number | string): Promise<void> {
  savingKey.value = key;
  const r = await api.put<SettingWritten>(`/api/settings/${key}`, { value });
  savingKey.value = null;
  if (!r.ok) {
    hostError.value = problem(r, `save ${key}`);
    return;
  }
  hostError.value = '';
  // The WHOLE row from the response, not just the number: a stored value's source is now "db", and
  // a line still saying "from the environment" is the precise lie this display exists to prevent.
  if (host.value) {
    host.value.settings = host.value.settings.map((s) =>
      s.key === key ? { key: s.key, value: r.body.value, source: r.body.source, minRole: r.body.minRole } : s,
    );
  }
  resetDrafts();
  // The server's `note` says what a cap change did and did not do; that fact is on the page beside
  // the field, so the toast stays a toast.
  toast('ok', 'Saved.');
}

/**
 * May this account write this key — for rendering only.
 *
 * FAILS CLOSED on a `minRole` this build does not know: `rank()` scores an unfamiliar role 0, so
 * handing one to `can()` unchecked would answer true for every signed-in account, and the least
 * privileged person on the host would be shown a control the server refuses.
 */
function mayWrite(r: SettingRow | null): boolean {
  if (!r) return false;
  return can((ROLES as readonly string[]).includes(r.minRole) ? (r.minRole as Role) : 'admin');
}

/** Empty when they may write it; otherwise the sentence rendered under the disabled control. */
function whyReadOnly(r: SettingRow | null): string {
  if (!r || mayWrite(r)) return '';
  return `Needs the ${r.minRole} role.`;
}

// Where the value came from — a fact about the host, never an instruction: a viewer reads this line
// too, and whoever set it may not be the person looking. Precedence itself lives in docs/usage.md.
const SOURCE_LABEL: Record<SettingRow['source'], string> = {
  db: 'Set here',
  env: 'From the environment',
  default: 'The shipped default',
};

const running = computed(() => state.jobs.filter((j) => j.state === 'running').length);
const waiting = computed(() => state.jobs.filter((j) => j.state === 'queued').length);

/**
 * Why the cap cannot be saved as typed — shown as the button's title, per the disabled rule.
 *
 * The rule itself lives in `useJobQueue` because this app has no component harness: logic that
 * would sit inline in a `.vue` is untestable there, and this one shipped broken.
 */
const maxJobsProblem = computed(() => capProblem(maxJobsDraft.value));
/** `Number` on both sides: the input hands back a string, so `'4' !== 4` would leave Save lit forever. */
const maxJobsDirty = computed(() => Number(maxJobsDraft.value) !== Number(maxJobs.value?.value));
const roleDirty = computed(() => roleDraft.value !== String(defaultRole.value?.value ?? ''));

/** One line each, in the picker itself — a role name alone does not say what it costs to grant. */
const ROLE_WHAT: Record<Role, string> = {
  viewer: 'reads everything',
  developer: 'deploys and tears down',
  maintainer: 'host configuration',
  admin: 'accounts and sign-on',
};
const roleOptions = ROLES.map((r) => ({ value: r, label: sentence(r), hint: ROLE_WHAT[r] }));
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Settings</h1>
        <!-- No page-level subtitle: two panels are this browser's and one is the host's, so each
             says which in its own line — a claim at the top would be a lie about one of them. -->
      </div>
    </div>

    <!-- `settings-form` caps the field widths: a URL input stretched to the panel's full 1500px
         reads as a text editor, not a form — see the rule in app.css. -->
    <section class="panel settings-form">
      <h2 class="phead-title">Connection</h2>
      <p class="mute hint">Stored in this browser. Nothing here is sent anywhere.</p>

      <div class="field">
        <label for="apiBase">API base URL</label>
        <input
          id="apiBase"
          v-model.trim="settings.apiBase"
          type="url"
          spellcheck="false"
          placeholder="(same origin)"
        />
        <div class="mute hint">Empty means same origin.</div>
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
          Only needed for token-based access; signing in does the same job.
          <template v-if="authEnforced === false">This server is not asking for one.</template>
        </div>
      </div>

      <div class="row" style="gap: 8px; margin-top: 4px">
        <button class="btn" :disabled="checking" @click="recheck">
          {{ checking ? 'Checking…' : 'Test connection' }}
        </button>
        <span v-if="state.health" class="mute">
          Connected to pstack {{ state.health.version }}
          <InfoHint label="where data is stored">
            Data directory: <code>{{ state.health.dataDir }}</code>
          </InfoHint>
        </span>
        <span v-else-if="state.healthError" class="bad">{{ state.healthError }}</span>
      </div>
    </section>

    <!-- ===================== the host's own settings — everyone sees these ===================== -->
    <section class="panel settings-form">
      <h2 class="phead-title">This host</h2>
      <p class="mute hint">Stored on the server, in force for everybody.</p>

      <ErrorNote v-if="hostError" :text="hostError" title="Host settings." />

      <template v-if="host">
        <div class="field">
          <!-- The dimming is inline because app.css has no `input:disabled` rule — `button:disabled`
               exists (which is what fades the role picker, a real <button> under the hood) and
               checkboxes have one, but this is the app's first disabled TEXT-shaped input, and
               without it a viewer gets a box that looks live, takes no focus and explains nothing.
               It matches the button rule's 0.45; the honest home for it is app.css, which is not
               this agent's to edit. -->
          <label for="maxJobs">Concurrency limit</label>
          <div class="row">
            <input
              id="maxJobs"
              v-model="maxJobsDraft"
              type="number"
              min="1"
              step="1"
              inputmode="numeric"
              :disabled="!mayWrite(maxJobs)"
              :title="whyReadOnly(maxJobs) || undefined"
              :style="`width: 8rem${mayWrite(maxJobs) ? '' : '; opacity: 0.45; cursor: not-allowed'}`"
            />
            <ActionButton
              variant="primary"
              :pending="savingKey === 'max_jobs'"
              :disabled="!mayWrite(maxJobs) || !maxJobsDirty || !!maxJobsProblem || savingKey !== null"
              :title="whyReadOnly(maxJobs) || maxJobsProblem || (maxJobsDirty ? undefined : 'this is already the stored value')"
              @click="save('max_jobs', Number(maxJobsDraft))"
            >
              Save
            </ActionButton>
            <span class="mute" style="font-size: var(--t-xs)">
              {{ running }} running · {{ waiting }} waiting
            </span>
          </div>
          <div class="mute hint">
            Most jobs at once, across every stack. <b>Lowering it cancels nothing.</b>
          </div>
          <div v-if="maxJobs" class="mute hint">
            <b>{{ SOURCE_LABEL[maxJobs.source] }}.</b> {{ whyReadOnly(maxJobs) }}
          </div>
        </div>

        <div class="field">
          <label for="defaultRole">Default role for new accounts</label>
          <div class="row">
            <SelectMenu
              id="defaultRole"
              v-model="roleDraft"
              label="Default role for new accounts"
              :options="roleOptions"
              :disabled="!mayWrite(defaultRole)"
            />
            <ActionButton
              variant="primary"
              :pending="savingKey === 'default_role'"
              :disabled="!mayWrite(defaultRole) || !roleDirty || savingKey !== null"
              :title="whyReadOnly(defaultRole) || (roleDirty ? undefined : 'this is already the stored value')"
              @click="save('default_role', roleDraft)"
            >
              Save
            </ActionButton>
          </div>
          <div class="mute hint">New accounts only; existing ones keep their role.</div>
          <div v-if="defaultRole" class="mute hint">
            <b>{{ SOURCE_LABEL[defaultRole.source] }}.</b> {{ whyReadOnly(defaultRole) }}
          </div>
        </div>
      </template>
      <p v-else-if="!hostLoaded" class="mute hint">Reading…</p>
    </section>

    <section class="panel">
      <h2 class="phead-title">Appearance</h2>
      <p class="mute hint">Stored in this browser.</p>

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
      </div>
    </section>
  </div>
</template>
