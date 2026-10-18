<script setup lang="ts">
/**
 * Apply a sealed host configuration. No export here: `GET /api/config` answers only the
 * PSTACK_TOKEN bearer, so a browser session cannot take one. Preview first; apply is confirmed.
 */
import { computed, ref } from 'vue';
import {
  HAlert,
  HAlertDialog,
  HButton,
  HCodeBlock,
  HDescriptionList,
  HFileUpload,
  HInput,
  HPageHeader,
  HStack,
  HText,
} from '@hearth-ui/vue';
import { failure, hostcfg } from '../api-hostcfg';
import { previewCounts, type Applied, type Preview } from '../lib/hostcfg';
import { can } from '../lib/users';
import { auth, toast } from '../state';

const files = ref<File[]>([]);
const sealed = ref('');
const passphrase = ref('');
const busy = ref<'' | 'preview' | 'apply'>('');
const error = ref('');
const forbidden = ref(false);
const preview = ref<Preview | null>(null);
const applied = ref<Applied | null>(null);
const confirm = ref(false);
const reset = ref(0);
const admin = computed(() => can('admin', auth));

function drop(): void {
  preview.value = null;
  applied.value = null;
  error.value = '';
}

/** Read in the browser; a new file clears any preview so it can never describe another file. */
async function pick(f: File[]): Promise<void> {
  files.value = f;
  drop();
  sealed.value = f[0] ? await f[0].text() : '';
}

async function run(mode: 'preview' | 'apply'): Promise<void> {
  error.value = '';
  busy.value = mode;
  const r = await hostcfg.sealed(sealed.value, passphrase.value, mode === 'preview');
  busy.value = '';
  confirm.value = false;
  if (!r.ok) {
    forbidden.value = r.status === 403;
    error.value = failure(r);
    return;
  }
  if (mode === 'preview') {
    preview.value = r.body as Preview;
    return;
  }
  const done = r.body as Applied;
  applied.value = done;
  preview.value = null;
  passphrase.value = '';
  files.value = [];
  sealed.value = '';
  reset.value++;
  toast('Applied', 'success', `${done.created.length} created, ${done.skipped.length} already here`);
}

const counts = computed(() => (preview.value ? previewCounts(preview.value).map((c) => ({ key: c.key, label: c.label, value: c.value })) : []));
const ready = computed(() => admin.value && !!sealed.value && !!passphrase.value && !busy.value);
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader title="Apply a configuration" description="A sealed export from another host. Creates what is missing; never overwrites." />

    <HAlert v-if="auth.checked && !admin" tone="info" title="Admin only" description="Applying a configuration needs an admin." />

    <HStack direction="column" :gap="3" class="narrow">
      <HFileUpload
        label="Sealed export"
        description="Up to 8 MiB."
        accept=".sealed,.yaml,.yml,.json,application/json"
        :model-value="files"
        :reset-key="reset"
        :max-size="8388608"
        @change="pick"
        @reject="(r) => (error = r.map((x) => `${x.name}: ${x.reason}`).join('; '))"
      />
      <HInput
        v-model="passphrase"
        label="Passphrase"
        type="password"
        autocomplete="off"
        hint="Sent to this host to open the file."
        @update:model-value="drop"
      />
      <HStack direction="row" :gap="2" wrap>
        <HButton variant="secondary" label="Preview" :loading="busy === 'preview'" :disabled="!ready" @click="run('preview')" />
        <HButton variant="danger" label="Apply" :loading="busy === 'apply'" :disabled="!ready || !preview" @click="confirm = true" />
      </HStack>
    </HStack>

    <HAlert v-if="error" tone="danger" :title="forbidden ? 'Forbidden' : 'Not applied'" :description="error" />

    <HStack v-if="preview" direction="column" :gap="3" class="narrow">
      <HAlert tone="warning" title="This file makes this host trust" description="Nothing is written yet.">
        <ul v-if="preview.trusts.length">
          <li v-for="t in preview.trusts" :key="t">{{ t }}</li>
        </ul>
        <span v-else>Nothing new.</span>
      </HAlert>
      <HDescriptionList :items="counts" :columns="2" dense />
    </HStack>

    <HStack v-if="applied" direction="column" :gap="2" class="narrow">
      <HAlert tone="success" title="Applied" />
      <template v-if="applied.created.length">
        <HText weight="bold">Created</HText>
        <HCodeBlock :code="applied.created.join('\n')" />
      </template>
      <template v-if="applied.skipped.length">
        <HText weight="bold" tone="muted">Already here</HText>
        <HCodeBlock :code="applied.skipped.join('\n')" />
      </template>
    </HStack>

    <HText size="sm" tone="muted">
      Take an export with <code>pstack pull config -o host.sealed</code> using the host's <code>PSTACK_TOKEN</code>.
    </HText>

    <HAlertDialog
      :open="confirm"
      title="Apply this configuration?"
      description="It can add admins, tokens and registries."
      confirm-label="Apply"
      danger
      :busy="busy === 'apply'"
      @cancel="confirm = false"
      @confirm="run('apply')"
    />
  </HStack>
</template>

<style scoped>
.narrow {
  max-width: 560px;
}
</style>
