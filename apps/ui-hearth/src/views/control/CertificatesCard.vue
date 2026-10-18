<script setup lang="ts">
import { computed, ref } from 'vue';
import { HAlert, HAlertDialog, HBadge, HButton, HCard, HDescriptionList, HStack, HText, HTextarea } from '@hearth-ui/vue';
import { controlApi, why, type TlsRedeploy, type TlsStatus } from '../../api-control';
import { daysLeft } from '../../lib/control';
import { stamp } from '../../format';
import { toast } from '../../state';

const props = defineProps<{ tls: TlsStatus; canAdmin: boolean }>();
const emit = defineEmits<{ reload: [] }>();

const cert = ref('');
const key = ref('');
const storing = ref(false);
const removing = ref(false);
const confirmRemove = ref(false);
const redeploying = ref(false);
const redeployed = ref<TlsRedeploy | null>(null);

const days = computed(() => (props.tls.wildcard ? daysLeft(props.tls.wildcard.notAfter) : null));
const items = computed(() => {
  const w = props.tls.wildcard;
  if (!w) return [];
  return [
    { key: 'covers', label: 'Covers', value: w.domains.join(', ') },
    { key: 'until', label: 'Valid until', value: stamp(w.notAfter) },
    { key: 'issuer', label: 'Issuer', value: w.issuer || 'unnamed' },
  ];
});

async function store(): Promise<void> {
  storing.value = true;
  const r = await controlApi.storeWildcard(cert.value, key.value);
  storing.value = false;
  if (!r.ok) {
    toast(why(r, 'Store the wildcard'), 'danger');
    return;
  }
  cert.value = key.value = '';
  redeployed.value = null;
  toast('Stored. Redeploy all stacks.', 'success');
  emit('reload');
}
async function remove(): Promise<void> {
  removing.value = true;
  const r = await controlApi.removeWildcard();
  removing.value = false;
  confirmRemove.value = false;
  if (!r.ok) {
    toast(why(r, 'Remove the wildcard'), 'danger');
    return;
  }
  redeployed.value = null;
  toast('Removed. Redeploy all stacks.', 'success');
  emit('reload');
}
async function redeploy(): Promise<void> {
  redeploying.value = true;
  const r = await controlApi.redeploy();
  redeploying.value = false;
  if (!r.ok) {
    toast(why(r, 'Redeploy the stacks'), 'danger');
    return;
  }
  redeployed.value = r.body;
  toast(`Redeploying ${r.body.started.length}, skipped ${r.body.skipped.length}.`, 'success');
}
</script>

<template>
  <HCard title="Certificates">
    <HStack :gap="4" direction="column">
      <HStack direction="row" :gap="2" wrap align="center">
        <HBadge :tone="tls.mode === 'dns-persist-01' ? 'success' : 'info'" :label="tls.mode" />
        <HBadge v-if="days !== null" :tone="days < 21 ? 'danger' : 'neutral'" :label="`${days} days left`" />
        <HBadge v-if="tls.wildcard?.selfSigned" tone="warning" label="Self-signed" />
      </HStack>
      <HDescriptionList v-if="items.length" :items="items" variant="inline" />
      <HAlert v-if="tls.wildcard?.selfSigned" tone="warning" title="Browsers will warn." compact />
      <HStack direction="row" v-if="tls.wildcard && canAdmin" :gap="3" align="center" wrap>
        <HButton variant="danger" :loading="removing" @click="confirmRemove = true">Remove wildcard</HButton>
        <HText tone="muted">Stacks under it serve no certificate until redeployed.</HText>
      </HStack>

      <template v-if="canAdmin">
        <HTextarea v-model="cert" label="Certificate (PEM, leaf first)" :rows="5" placeholder="-----BEGIN CERTIFICATE-----" hint="Stored 0600. Never returned." />
        <HTextarea v-model="key" label="Private key (PEM)" :rows="4" placeholder="-----BEGIN PRIVATE KEY-----" />
        <div>
          <HButton variant="primary" :loading="storing" :disabled="!cert.trim() || !key.trim()" @click="store">
            {{ tls.wildcard ? 'Replace wildcard' : 'Store wildcard' }}
          </HButton>
        </div>
      </template>
      <HText v-else-if="!tls.wildcard" tone="muted">Admin only.</HText>

      <HStack direction="row" :gap="3" align="center" wrap>
        <HButton :loading="redeploying" @click="redeploy">Redeploy all stacks</HButton>
        <HText tone="muted">Asleep stacks pick up new labels on wake.</HText>
      </HStack>
      <HAlert
        v-if="redeployed"
        tone="info"
        :title="`Started ${redeployed.started.length}, skipped ${redeployed.skipped.length}`"
        :description="redeployed.skipped.map((x) => `${x.id}: ${x.reason}`).join('; ') || undefined"
        compact
      />
    </HStack>
    <HAlertDialog
      v-model:open="confirmRemove"
      danger
      title="Remove the wildcard?"
      description="Deployed stacks lose TLS."
      confirm-label="Remove"
      :busy="removing"
      @confirm="remove"
    />
  </HCard>
</template>
