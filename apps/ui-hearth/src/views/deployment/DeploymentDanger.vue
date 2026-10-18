<script setup lang="ts">
/**
 * Sleep, share, tear down, stop, forget. Deploy and Verify are in the page header; this section is
 * what destroys something or mints a credential, and every destructive button here asks first.
 *
 * Two guards matter. A shared tear down needs the stack name typed out (a checkbox is one stray
 * click; the server also refuses without `force`). Forgetting removes the record only and never
 * touches a container; the server refuses while any exists, or while Docker cannot confirm.
 */
import { computed, reactive, ref, watch } from 'vue';
import { HAlert, HAlertDialog, HButton, HCard, HCheckbox, HCopyField, HInput, HSelect, HStack, HText } from '@hearth-ui/vue';
import { deploymentApi, sharePath, type DeploymentDetail } from '../../api-deployment';
import { refresh, toast } from '../../state';
import { problemText, SHARE_TTLS, shareViews } from '../../lib/deployment';
import type { DeploymentActions } from '../../lib/use-deployment-actions';
import type { ShareLink } from '@samyx/preview-stacks-client';
import ConflictNote from './ConflictNote.vue';
import EquivalentCommand from './EquivalentCommand.vue';

const props = defineProps<{ id: string; dep: DeploymentDetail | null; actions: DeploymentActions }>();
const a = props.actions;

const shared = computed(() => props.dep?.kind === 'shared');
const stack = computed(() => props.dep?.stack ?? '');

// ── sleep ───────────────────────────────────────────────────────────────────────────────────────
const sleepWhy = computed(() => a.why('sleep'));

// ── share: a credential, shown once, held in this component only ────────────────────────────────
const shareDetails = ref(true);
const shareLogs = ref(true);
const ttl = ref('7d');
const link = ref<ShareLink | null>(null);
const shareError = ref('');
const sharing = ref(false);
const views = computed(() => shareViews(shareDetails.value, shareLogs.value));

async function createLink(): Promise<void> {
  sharing.value = true;
  shareError.value = '';
  link.value = null;
  const r = await deploymentApi.share(props.id, views.value, ttl.value);
  sharing.value = false;
  if (r.status === 201) link.value = r.body;
  else shareError.value = problemText(r.status, r.body.error, 'Share link');
}

// ── tear down ───────────────────────────────────────────────────────────────────────────────────
const verify = ref(true);
const typed = ref('');
// Exact match against the resolved stack name. Nothing looser.
const armed = computed(() => !!stack.value && typed.value === stack.value);
const downWhy = computed(() => a.why('down', armed.value));

const interrupted = ref(false);
const stopping = ref(false);
async function stopEverything(): Promise<void> {
  stopping.value = true;
  const r = await deploymentApi.cancel(props.id);
  stopping.value = false;
  if (!r.ok) {
    toast('Stop refused', 'danger', problemText(r.status, r.body.error, 'Stop'));
    return;
  }
  const n = r.body.cancelled.length;
  toast(n ? `Stopped ${n} job${n > 1 ? 's' : ''}` : 'Nothing was running or queued', n ? 'success' : 'info');
  // Cancelling a running job undoes nothing it already did; a queued one never ran.
  interrupted.value = r.body.cancelled.some((j) => j.state === 'running');
  void refresh();
}

// ── one dialog for every destructive button ─────────────────────────────────────────────────────
type Ask = 'down' | 'down-forget' | 'stop' | 'forget';
const ask = reactive({ open: false, kind: 'down' as Ask });
const COPY: Record<Ask, { title: string; text: string; confirm: string }> = {
  down: { title: 'Tear down', text: 'Containers, networks and volumes are removed.', confirm: 'Tear down' },
  'down-forget': { title: 'Tear down and forget', text: 'Tears down, then removes the record if nothing is left behind.', confirm: 'Tear down and forget' },
  stop: { title: 'Stop everything on this stack', text: 'Cancels the running job and the one queued behind it. Nothing is undone.', confirm: 'Stop everything' },
  forget: { title: 'Forget this deployment', text: 'Removes the record only. Containers are not touched.', confirm: 'Forget' },
};
function open(kind: Ask): void {
  ask.kind = kind;
  ask.open = true;
}
function confirmed(): void {
  ask.open = false;
  const force = shared.value && armed.value;
  if (ask.kind === 'down') void a.act('down', { verify: verify.value, force });
  else if (ask.kind === 'down-forget') void a.act('down', { verify: true, force, forget: true });
  else if (ask.kind === 'stop') void stopEverything();
  else void a.forgetRecord();
}

watch(
  () => props.id,
  () => {
    link.value = null;
    shareError.value = '';
    typed.value = '';
    interrupted.value = false;
  },
);
</script>

<template>
  <HStack direction="column" :gap="4">
    <HAlert v-if="!dep" tone="warning" title="Needs this deployment's variables" description="Open Config to set them." />

    <HCard v-if="dep && !shared">
      <template #header><HText weight="medium">Sleep</HText></template>
      <HStack direction="column" :gap="3">
        <HText tone="muted">Takes the {{ dep.orchestrator === 'swarm' ? 'swarm stack' : 'compose project' }} down and keeps its volumes. A request to its hostnames wakes it.</HText>
        <HStack direction="row" :gap="3" wrap align="center">
          <HButton label="Sleep now" icon="moon" :loading="a.pending.value === 'sleep'" :disabled="!!a.pending.value || !!sleepWhy" @click="a.act('sleep')" />
          <HText v-if="sleepWhy" tone="muted" size="sm">{{ sleepWhy }}</HText>
        </HStack>
      </HStack>
    </HCard>

    <HCard v-if="dep">
      <template #header>
        <HStack direction="row" :gap="3" wrap align="center" justify="between">
          <HText weight="medium">Share a read-only link</HText>
          <EquivalentCommand method="POST" :path="sharePath(id)" :body="{ views, ttl }" />
        </HStack>
      </template>
      <HStack direction="column" :gap="3">
        <HText tone="muted">Read-only, this deployment only. No per-link revoke: rotating PSTACK_TOKEN ends every link.</HText>
        <HStack direction="row" :gap="4" wrap align="end">
          <HCheckbox v-model="shareDetails" label="Details and containers" />
          <HCheckbox v-model="shareLogs" label="Logs" />
          <HSelect v-model="ttl" label="Expires in" :options="SHARE_TTLS" />
          <HButton label="Create link" :loading="sharing" :disabled="sharing || !views.length" @click="createLink" />
        </HStack>
        <HText v-if="!views.length" tone="muted" size="sm">Pick at least one view.</HText>
        <HAlert v-if="shareError" tone="danger" :title="shareError" />
        <HAlert v-if="link" tone="success" title="Shown once. Copy it now." :description="`Opens ${link.views.join(' and ')} until ${new Date(link.expiresAt).toLocaleString()}.`" dismissible @dismiss="link = null">
          <HCopyField label="Share link" :value="link.url" secret />
          <HButton variant="ghost" label="Open" icon="launch" :href="link.url" target="_blank" />
        </HAlert>
      </HStack>
    </HCard>

    <HCard v-if="dep">
      <template #header><HText weight="medium">Tear down</HText></template>
      <HStack direction="column" :gap="3">
        <HText tone="muted">Stops and removes this deployment's containers, networks and volumes.</HText>

        <HAlert v-if="shared" tone="warning" title="Shared stack" description="This deletes the volumes every preview on this host depends on. Nothing recreates them.">
          <HInput v-model="typed" :label="`Type ${stack} to confirm`" :placeholder="stack" autocomplete="off" :error="typed && !armed ? 'Does not match.' : undefined" />
        </HAlert>

        <HStack direction="row" :gap="3" wrap align="center">
          <HButton variant="danger" :label="shared ? 'Tear down (force)' : 'Tear down'" :loading="a.pending.value === 'down'" :disabled="!!a.pending.value || !!downWhy" @click="open('down')" />
          <!-- One job: the teardown, then the record, only if it finished clean. Needs the leftovers check, or a leak is hidden. -->
          <HButton variant="danger" label="Tear down and forget" :loading="a.pending.value === 'down-forget'" :disabled="!!a.pending.value || !!downWhy || !verify" @click="open('down-forget')" />
          <HCheckbox v-model="verify" label="Check for leftovers afterwards" />
        </HStack>
        <HText v-if="downWhy" tone="muted" size="sm">{{ downWhy }}</HText>
        <HText v-if="!verify" tone="muted" size="sm">Nothing will check that the teardown finished. Forgetting needs the check.</HText>
        <HText v-if="a.busy.value" tone="muted" size="sm">A job is running on {{ stack }}. Tearing down cancels it first.</HText>
        <HText tone="muted" size="sm">Tear down and forget keeps the record if anything is left behind.</HText>

        <HStack direction="row" :gap="3" wrap align="center">
          <HButton variant="danger" label="Stop everything on this stack" :loading="stopping" :disabled="stopping" @click="open('stop')" />
          <HText tone="muted" size="sm">Destroys nothing. Whatever already ran stays done.</HText>
        </HStack>
        <HAlert v-if="interrupted" tone="warning" title="Stopped mid-run" description="What it created before stopping is still there. Run Verify." compact />

        <HAlert v-if="a.error.value" tone="danger" :title="a.error.value" />
        <ConflictNote v-if="a.conflict.value" :conflict="a.conflict.value" :job-id="a.conflictJobId.value" />
      </HStack>
    </HCard>

    <HCard v-if="dep">
      <template #header><HText weight="medium">Forget this deployment</HText></template>
      <HStack direction="column" :gap="3">
        <HText tone="muted">Removes the record only. It never tears anything down. Refused while any container exists, or while Docker cannot confirm.</HText>
        <div>
          <HButton variant="danger" label="Forget" :loading="a.pending.value === 'forget'" :disabled="!!a.pending.value" @click="open('forget')" />
        </div>
      </HStack>
    </HCard>

    <HAlertDialog
      v-model:open="ask.open"
      danger
      :title="`${COPY[ask.kind].title}${ask.kind === 'stop' ? '?' : ` ${id}?`}`"
      :description="COPY[ask.kind].text"
      :confirm-label="COPY[ask.kind].confirm"
      @confirm="confirmed"
    />
  </HStack>
</template>
