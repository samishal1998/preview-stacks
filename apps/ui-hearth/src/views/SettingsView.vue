<script setup lang="ts">
/**
 * Host settings (stored on the server, for everybody) and this browser's appearance.
 * The concurrency cap needs maintainer, the default role needs admin; each row says where its value came from.
 */
import { computed, ref } from 'vue';
import {
  HAlert,
  HButton,
  HCard,
  HEmptyState,
  HInput,
  HSelect,
  HSettingsPage,
  HSkeleton,
  HStack,
  HText,
  HThemeSwitcher,
} from '@hearth-ui/vue';
import type { HostSettings, SettingKey, SettingRow } from '@samyx/preview-stacks-client';
import { settingsApi } from '../api-settings';
import { SOURCE_LABEL, capProblem, densities, mayWrite, palettes } from '../lib/settings';
import { problem } from '../lib/settings-problem';
import { roleOptions } from '../lib/users';
import { getToken, tokenShape } from '../lib/token';
import { auth, data, forgetToken, prefs, toast } from '../state';

const host = ref<HostSettings | null>(null);
const loaded = ref(false);
const forbidden = ref(false);
const error = ref('');
const saving = ref(false);

const maxJobsDraft = ref('');
const roleDraft = ref('viewer');

const row = (k: SettingKey): SettingRow | null => host.value?.settings.find((s) => s.key === k) ?? null;
const maxJobs = computed(() => row('max_jobs'));
const defaultRole = computed(() => row('default_role'));
const may = (r: SettingRow | null) => !!r && mayWrite(r.minRole, auth.role, auth.root);
const why = (r: SettingRow | null) => (r && !may(r) ? `Needs the ${r.minRole} role.` : '');
const source = (r: SettingRow | null) => (r ? `${SOURCE_LABEL[r.source]}.` : '');

const capError = computed(() => (maxJobsDirty.value ? capProblem(maxJobsDraft.value) : ''));
const maxJobsDirty = computed(() => may(maxJobs.value) && maxJobsDraft.value.trim() !== String(maxJobs.value?.value ?? ''));
const roleDirty = computed(() => may(defaultRole.value) && roleDraft.value !== String(defaultRole.value?.value ?? ''));
const dirty = computed(() => maxJobsDirty.value || roleDirty.value);

const running = computed(() => data.jobs.filter((j) => j.state === 'running').length);
const waiting = computed(() => data.jobs.filter((j) => j.state === 'queued').length);

function reset(): void {
  maxJobsDraft.value = String(maxJobs.value?.value ?? '');
  roleDraft.value = String(defaultRole.value?.value ?? 'viewer');
}

async function load(): Promise<void> {
  const r = await settingsApi.read();
  loaded.value = true;
  forbidden.value = r.status === 403;
  if (!r.ok) {
    error.value = forbidden.value ? '' : problem(r, 'Could not read host settings');
    return;
  }
  error.value = '';
  host.value = r.body;
  reset();
}
void load();

async function save(): Promise<void> {
  if (capError.value) {
    error.value = capError.value;
    return;
  }
  saving.value = true;
  error.value = '';
  let failed = false;
  const writes: Array<[SettingKey, number | string]> = [];
  if (maxJobsDirty.value) writes.push(['max_jobs', Number(maxJobsDraft.value)]);
  if (roleDirty.value) writes.push(['default_role', roleDraft.value]);
  for (const [key, value] of writes) {
    const r = await settingsApi.write(key, value);
    if (!r.ok) {
      error.value = problem(r, `Could not save ${key}`);
      failed = true;
      break;
    }
    // The whole row: its source is now "db".
    if (host.value) {
      host.value.settings = host.value.settings.map((s) =>
        s.key === key ? { key, value: r.body.value, source: r.body.source, minRole: r.body.minRole } : s,
      );
    }
    toast('Saved', 'success');
  }
  saving.value = false;
  // A failed write keeps the drafts so they can be fixed and retried.
  if (!failed) reset();
}
</script>

<template>
  <HSettingsPage
    title="Settings"
    description="Host settings apply to everybody. Appearance is per browser."
    :dirty="dirty"
    :saving="saving"
    :error="host ? error : ''"
    save-label="Save"
    @save="save"
    @reset="reset"
  >
    <HStack direction="column" :gap="4">
      <HEmptyState v-if="forbidden" icon="lock" title="Not allowed" description="Your account cannot read host settings." />
      <HCard v-else title="This host" description="Stored on the server.">
        <HSkeleton v-if="!loaded" variant="text" :lines="3" label="Loading settings" />
        <HAlert v-else-if="!host" tone="danger" title="Settings unavailable" :description="error" />
        <HStack v-else direction="column" :gap="4" style="max-width: 28rem">
          <HInput
            v-model="maxJobsDraft"
            type="number"
            min="1"
            step="1"
            label="Concurrency limit"
            :disabled="!may(maxJobs)"
            :error="capError"
            :hint="`Most jobs at once. Lowering it cancels nothing. ${running} running, ${waiting} waiting. ${source(maxJobs)} ${why(maxJobs)}`"
          />
          <HSelect
            v-model="roleDraft"
            label="Default role for new accounts"
            :options="roleOptions"
            :disabled="!may(defaultRole)"
            :hint="`Existing accounts keep their role. ${source(defaultRole)} ${why(defaultRole)}`"
          />
        </HStack>
      </HCard>

      <HCard v-if="auth.viaToken" title="Connection" description="Stored in this browser.">
        <HStack direction="row" :gap="2" align="center" wrap>
          <HText>{{ tokenShape(getToken()) }}</HText>
          <HButton variant="secondary" size="compact" label="Forget token" @click="forgetToken" />
        </HStack>
      </HCard>

      <HCard title="Appearance" description="Stored in this browser.">
        <HStack direction="column" :gap="3" style="max-width: 28rem">
          <HThemeSwitcher v-model="prefs.mode" />
          <HSelect v-model="prefs.theme" label="Palette" :options="palettes" />
          <HSelect v-model="prefs.density" label="Density" :options="densities" />
        </HStack>
      </HCard>
    </HStack>
  </HSettingsPage>
</template>
