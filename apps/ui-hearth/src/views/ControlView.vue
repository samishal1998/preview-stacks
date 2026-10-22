<script setup lang="ts">
/**
 * The control stack as an operator debugs it. Restarts and OOM lead because a restarting Traefik
 * loses in-flight certificate issuance while every container still reads `running`.
 */
import { computed, ref } from 'vue';
import { HAlert, HButton, HGrid, HPageHeader, HStack, HStatCard } from '@hearth-ui/vue';
import { controlApi, poll, why, type ControlRuntime, type DomainsStatus, type TlsStatus } from '../api-control';
import { atLeast, daysLeft, type RoleName } from '../lib/control';
import { auth } from '../state';
import ContainersCard from './control/ContainersCard.vue';
import CertificatesCard from './control/CertificatesCard.vue';
import DomainsCard from './control/DomainsCard.vue';
import LoggingCard from './control/LoggingCard.vue';

const can = (min: RoleName) => atLeast(auth.root, auth.role, min);

const runtime = ref<ControlRuntime | null>(null);
const loaded = ref(false);
const error = ref('');
const forbidden = ref(false);
const domains = ref<DomainsStatus | null>(null);
const tls = ref<TlsStatus | null>(null);

async function loadRuntime(): Promise<void> {
  // Below maintainer every call here is a 403: say so without asking (and without logging one every poll).
  if (!can('maintainer')) {
    loaded.value = forbidden.value = true;
    return;
  }
  const r = await controlApi.runtime();
  loaded.value = true;
  forbidden.value = r.status === 403;
  if (!r.ok) {
    error.value = forbidden.value ? '' : why(r, 'Read the control stack');
    return;
  }
  error.value = '';
  runtime.value = { ...r.body, containers: r.body.containers ?? [] };
}
async function loadDomains(): Promise<void> {
  if (!can('maintainer')) return;
  const r = await controlApi.domains();
  if (r.ok) domains.value = { ...r.body, domains: r.body.domains ?? [] };
}
async function loadTls(): Promise<void> {
  if (!can('maintainer')) return;
  const r = await controlApi.tls();
  if (r.ok) tls.value = r.body;
}
const reloadDomains = () => Promise.all([loadDomains(), loadTls()]);
const refreshing = ref(false);
async function reloadAll(): Promise<void> {
  refreshing.value = true;
  await Promise.all([loadRuntime(), loadDomains(), loadTls()]);
  refreshing.value = false;
}
poll(loadRuntime, 10_000);
poll(loadDomains, 30_000);
poll(loadTls, 30_000);

const restarts = computed(() => (runtime.value?.containers ?? []).reduce((n, c) => n + c.restartCount, 0));
const oomed = computed(() => (runtime.value?.containers ?? []).filter((c) => c.oomKilled).length);
const days = computed(() => (tls.value?.wildcard ? daysLeft(tls.value.wildcard.notAfter) : null));
// The primary is only there once init has set one, so it is counted rather than assumed.
const domainCount = computed(() => (domains.value ? domains.value.domains.length + (domains.value.primary ? 1 : 0) : 0));
</script>

<template>
  <HStack :gap="4" direction="column">
    <HPageHeader title="Control stack" eyebrow="Host" description="Containers, hostnames, certificates and logging." actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="arrow" label="Refresh" :loading="refreshing" @click="reloadAll" />
      </template>
    </HPageHeader>

    <HAlert v-if="forbidden" tone="warning" title="Maintainer only." />
    <template v-else>
      <HGrid v-if="runtime?.reachable" min-column-width="8rem" :gap="3">
        <HStatCard label="Restarts" :value="restarts" :tone="restarts ? 'warning' : 'neutral'" />
        <HStatCard label="OOM killed" :value="oomed" :tone="oomed ? 'danger' : 'neutral'" />
        <HStatCard v-if="tls" label="Certificates" :value="tls.mode" tone="neutral" />
        <HStatCard v-if="days !== null" label="Cert expires" :value="`${days}d`" :tone="days < 21 ? 'danger' : 'neutral'" />
        <HStatCard v-if="domainCount" label="Domains" :value="domainCount" tone="neutral" />
      </HGrid>

      <ContainersCard :runtime="runtime" :loaded="loaded" :error="error" :can-restart="can('maintainer')" @reload="loadRuntime" />
      <DomainsCard v-if="domains" :domains="domains" :can-edit="can('maintainer')" @reload="reloadDomains" />
      <CertificatesCard v-if="tls" :tls="tls" :can-admin="can('admin')" @reload="loadTls" />
      <LoggingCard v-if="can('maintainer')" :can-admin="can('admin')" />
    </template>
  </HStack>
</template>
