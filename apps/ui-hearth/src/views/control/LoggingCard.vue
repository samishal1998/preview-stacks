<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { RouterLink } from 'vue-router';
import { HAlert, HAlertDialog, HBadge, HButton, HCard, HInput, HNumberInput, HSelect, HSettingsPage, HStack, HSwitch, HText } from '@hearth-ui/vue';
import type { Job, LokiSettings, LokiStorageInput } from '@samyx/preview-stacks-client';
import { controlApi, id, poll, why } from '../../api-control';
import { isTerminal, rangeProblem, storageBadge, supersededBy } from '../../lib/control';
import { data, toast } from '../../state';

const props = defineProps<{ canAdmin: boolean }>();

const logging = ref<LokiSettings | null>(null);
const loaded = ref(false);
const loadError = ref('');
const saveError = ref('');
const saving = ref(false);
/** The apply job a save started, or the one that superseded it, until it ends. */
const applying = ref<string | null>(null);

type Chunks = { retentionDays: number | null; idlePeriodMinutes: number | null; maxAgeMinutes: number | null; targetSizeKiB: number | null; encoding: string };
const blank = (): Chunks => ({ retentionDays: null, idlePeriodMinutes: null, maxAgeMinutes: null, targetSizeKiB: null, encoding: '' });
const draft = ref<Chunks>(blank());
const saved = ref<Chunks>(blank());
const s3 = ref({ endpoint: '', region: '', bucket: '', pathStyle: false, accessKeyId: '' });
const secret = ref('');
const wantS3 = ref(false);
const confirmS3 = ref(false);

// Refill only when the SAVED values change: the poll must not wipe typing.
watch(
  () => logging.value && JSON.stringify([logging.value.retentionDays, logging.value.chunks, logging.value.storage]),
  () => {
    const l = logging.value;
    if (!l) return;
    saved.value = { retentionDays: l.retentionDays, ...l.chunks };
    draft.value = { ...saved.value };
    wantS3.value = l.storage.type === 's3';
    const x = l.storage.s3;
    s3.value = { endpoint: x?.endpoint ?? '', region: x?.region ?? '', bucket: x?.bucket ?? '', pathStyle: x?.pathStyle ?? false, accessKeyId: x?.accessKeyId ?? '' };
  },
);

const onS3 = computed(() => logging.value?.storage.type === 's3');
/** S3 is one-way and fixed once saved. */
const storageLocked = computed(() => onS3.value || !props.canAdmin);
const dirty = computed(() => JSON.stringify(draft.value) !== JSON.stringify(saved.value));
const lim = computed(() => logging.value?.limits);
const problems = computed(() => {
  const l = lim.value;
  if (!l) return {};
  const d = draft.value;
  return {
    retentionDays: rangeProblem(d.retentionDays, l.retentionDays),
    idlePeriodMinutes: rangeProblem(d.idlePeriodMinutes, l.idlePeriodMinutes),
    maxAgeMinutes: rangeProblem(d.maxAgeMinutes, l.maxAgeMinutes),
    targetSizeKiB: rangeProblem(d.targetSizeKiB, l.targetSizeKiB),
  };
});
const valid = computed(() => Object.values(problems.value).every((p) => !p) && !!draft.value.encoding);
const encodings = computed(() => (lim.value?.encodings ?? []).map((e) => ({ value: e, label: e })));
const s3Ready = computed(() => !!(s3.value.endpoint.trim() && s3.value.region.trim() && s3.value.bucket.trim() && s3.value.accessKeyId.trim() && (onS3.value || secret.value)));

/** One read of the followed job: keep following, follow its successor, or end with a toast. */
async function follow(jobId: string): Promise<void> {
  const r = await controlApi.job(jobId);
  if (applying.value !== jobId) return; // a newer save took over during the read
  if (r.status === 404) {
    applying.value = null;
    return;
  }
  if (!r.ok || !isTerminal(r.body.job.state)) return;
  const job: Job = r.body.job;
  if (job.state === 'superseded') {
    applying.value = supersededBy(job, data.jobs)?.id ?? jobId; // the successor carries this save
    return;
  }
  applying.value = null;
  if (job.state === 'ok') toast('Applied.', 'success');
  else if (job.state === 'cancelled') toast('Cancelled.', 'warning');
  else toast('Apply failed.', 'danger', `Job ${job.id}`);
}

async function load(): Promise<void> {
  if (applying.value) await follow(applying.value);
  const r = await controlApi.logging();
  loaded.value = true;
  if (r.ok) {
    logging.value = r.body;
    loadError.value = '';
  } else if (r.status !== 403) loadError.value = why(r, 'Read the logging settings');
  else loadError.value = 'Maintainer only.';
}
poll(load, 10_000);

async function save(r: ReturnType<typeof controlApi.saveLogging>): Promise<void> {
  saving.value = true;
  const res = await r;
  saving.value = false;
  if (!res.ok) {
    saveError.value = why(res, 'Save');
    return;
  }
  saveError.value = '';
  secret.value = '';
  if ('job' in res.body) {
    applying.value = res.body.job.id;
    toast('Applying.', 'info');
  } else toast('No change.', 'success');
  await load();
}

const saveChunks = () => {
  const d = draft.value;
  return save(
    controlApi.saveLogging({
      retentionDays: d.retentionDays,
      chunks: { idlePeriodMinutes: d.idlePeriodMinutes, maxAgeMinutes: d.maxAgeMinutes, targetSizeKiB: d.targetSizeKiB, encoding: d.encoding },
    }),
  );
};
const reset = () => (draft.value = { ...saved.value });
function saveStorage(): Promise<void> {
  const l = logging.value;
  if (!l) return Promise.resolve();
  const body: LokiStorageInput = {
    type: 's3',
    ...s3.value,
    secretAccessKey: secret.value, // '' keeps the stored secret
    cutover: l.storage.s3?.cutover ?? l.limits.earliestCutover,
  };
  confirmS3.value = false;
  return save(controlApi.saveStorage(body));
}
</script>

<template>
  <!-- Hidden when this host runs no Loki. -->
  <HCard v-if="!loaded || loadError || (logging && logging.enabled !== false)" title="Logging">
    <HAlert v-if="loadError" tone="danger" :title="loadError" compact />
    <HText v-else-if="!logging" tone="muted">Loading.</HText>
    <HAlert v-else-if="logging.enabled === null" tone="warning" title="Docker did not answer." compact />
    <HStack v-else :gap="4" direction="column">
      <HStack direction="row" :gap="2" align="center" wrap>
        <HBadge tone="info" :label="storageBadge(logging.storage.s3)" />
        <RouterLink v-if="applying" :to="`/jobs/${id(applying)}`"><HBadge tone="info" label="Applying" dot /></RouterLink>
      </HStack>

      <HSettingsPage
        class="chunks"
        title="Retention and chunks"
        description="Restarts Loki."
        :dirty="dirty"
        :saving="saving"
        :error="saveError || (dirty && !valid ? 'Fix the highlighted fields.' : '')"
        @save="valid ? saveChunks() : (saveError = 'Fix the highlighted fields.')"
        @reset="reset"
      >
        <HStack :gap="3" direction="column">
          <HNumberInput v-model="draft.retentionDays" label="Retention (days)" :min="lim!.retentionDays.min" :max="lim!.retentionDays.max" :error="problems.retentionDays" />
          <HNumberInput v-model="draft.idlePeriodMinutes" label="Chunk idle (min)" :min="lim!.idlePeriodMinutes.min" :max="lim!.idlePeriodMinutes.max" :error="problems.idlePeriodMinutes" />
          <HNumberInput v-model="draft.maxAgeMinutes" label="Chunk max age (min)" :min="lim!.maxAgeMinutes.min" :max="lim!.maxAgeMinutes.max" :error="problems.maxAgeMinutes" />
          <HNumberInput v-model="draft.targetSizeKiB" label="Chunk size (KiB)" :min="lim!.targetSizeKiB.min" :max="lim!.targetSizeKiB.max" :error="problems.targetSizeKiB" />
          <HSelect v-model="draft.encoding" label="Encoding" :options="encodings" />
        </HStack>
      </HSettingsPage>

      <HCard title="Storage" :description="storageLocked ? (canAdmin ? 'Fixed once saved.' : 'Admin only.') : 'One-way.'">
        <HStack :gap="3" direction="column">
          <HSwitch v-model="wantS3" label="S3" :disabled="storageLocked" />
          <template v-if="wantS3">
            <HInput v-model="s3.endpoint" type="url" label="Endpoint" :disabled="storageLocked" />
            <HInput v-model="s3.region" label="Region" :disabled="storageLocked" />
            <HInput v-model="s3.bucket" label="Bucket" :disabled="storageLocked" />
            <HSwitch v-model="s3.pathStyle" label="Path-style" :disabled="storageLocked" />
            <HInput v-model="s3.accessKeyId" label="Access key ID" autocomplete="off" :disabled="!canAdmin" />
            <HInput
              v-if="canAdmin"
              v-model="secret"
              type="password"
              label="Secret access key"
              autocomplete="new-password"
              :hint="logging.storage.s3?.secretSet ? 'Write-only. Leave empty to keep.' : 'Write-only.'"
            />
            <div v-if="canAdmin">
              <HButton v-if="onS3" :loading="saving" :disabled="!s3Ready" @click="saveStorage">Save keys</HButton>
              <HButton v-else :loading="saving" :disabled="!s3Ready" @click="confirmS3 = true">Switch to S3</HButton>
            </div>
          </template>
        </HStack>
        <HAlertDialog
          v-model:open="confirmS3"
          danger
          :title="`Switch to S3 from ${logging.limits.earliestCutover} UTC?`"
          description="One-way."
          confirm-label="Switch to S3"
          :busy="saving"
          @confirm="saveStorage"
        />
      </HCard>
    </HStack>
  </HCard>
</template>

<style scoped>
/* Hearth sizes a settings page title as a page heading; inside a card it is a section. */
.chunks :deep(h1) { font-size: 1.125rem; line-height: 1.4; }
</style>
