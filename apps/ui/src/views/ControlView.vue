<script setup lang="ts">
/**
 * The control stack as an operator debugs it — traefik, pstack, the optional advanced UI.
 *
 * The dashboard's card answers "is it up"; this page answers "why did it restart". The two columns
 * that earn it are RESTARTS and OOM: a Traefik restart silently wipes its in-memory certificate
 * challenges, so TLS stops issuing while every state here still reads `running`. That incident was
 * once diagnosed from a default certificate's timestamp; now it is a red badge on this table.
 *
 * One action, one refusal: any control service can be restarted except `pstack` itself — the
 * server refuses its own container by name, whoever asks, and this page does not offer it.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import type { ControlRuntime, DomainsStatus, TlsRedeploy, TlsStatus } from '../api/types';
import { usePolling } from '../composables/usePolling';
import { can } from '../composables/useAuth';
import { ago, sentence, stamp } from '../composables/useFormat';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import RefreshButton from '../components/RefreshButton.vue';
import SkeletonList from '../components/SkeletonList.vue';

const view = ref<ControlRuntime | null>(null);
const error = ref('');
const loaded = ref(false);

async function load(): Promise<void> {
  const r = await api.get<ControlRuntime>('/api/control/runtime');
  loaded.value = true;
  if (!r.ok) {
    error.value = problem(r, 'read the control stack');
    return;
  }
  error.value = '';
  view.value = { ...r.body, containers: r.body.containers ?? [] };
}
usePolling(load, 10_000);

const restarting = ref('');
async function restart(service: string): Promise<void> {
  restarting.value = service;
  const r = await api.post<{ container: string }>('/api/control/restart', { service });
  restarting.value = '';
  if (!r.ok) {
    toast('error', problem(r, `restart ${service}`));
    return;
  }
  toast('ok', `Restarting ${r.body.container}.`);
  await load();
}

/** 268435456 → "256 MiB"; null → "unlimited" (the server normalizes docker's 0 to null). */
function mem(bytes: number | null): string {
  if (bytes === null) return 'unlimited';
  if (bytes >= 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GiB`;
  return `${Math.round(bytes / (1024 * 1024))} MiB`;
}

// ── the hostnames this host answers on ────────────────────────────────────────────────────────────
const domains = ref<DomainsStatus | null>(null);
const newDomain = ref('');
const savingDomains = ref(false);

async function loadDomains(): Promise<void> {
  const r = await api.get<DomainsStatus>('/api/domains');
  if (r.ok) domains.value = { ...r.body, domains: r.body.domains ?? [] };
}
usePolling(loadDomains, 30_000);

/** The list replaces what is stored, so add and remove are the same call. */
async function saveDomains(next: string[]): Promise<void> {
  savingDomains.value = true;
  const r = await api.put<DomainsStatus>('/api/domains', { domains: next });
  savingDomains.value = false;
  if (!r.ok) {
    toast('error', problem(r, 'save the domains'));
    return;
  }
  newDomain.value = '';
  toast('ok', 'Domains saved.');
  await Promise.all([loadDomains(), loadTls()]);
}

const addDomain = () => saveDomains([...(domains.value?.domains ?? []), newDomain.value.trim()]);
const removeDomain = (d: string) => saveDomains((domains.value?.domains ?? []).filter((x) => x !== d));

// ── the certificate mode ──────────────────────────────────────────────────────────────────────────
const tls = ref<TlsStatus | null>(null);
async function loadTls(): Promise<void> {
  const r = await api.get<TlsStatus>('/api/tls');
  if (r.ok) tls.value = r.body;
}
usePolling(loadTls, 30_000);

// The two numbers that explain a control plane misbehaving, summed for the strip.
const restarts = computed(() => (view.value?.containers ?? []).reduce((n, c) => n + c.restartCount, 0));
const oomed = computed(() => (view.value?.containers ?? []).some((c) => c.oomKilled));
// The primary is a hostname, not an entry in `domains` — but it is only there once init has set
// one, so it is counted rather than assumed.
const domainCount = computed(() =>
  domains.value ? domains.value.domains.length + (domains.value.primary ? 1 : 0) : 0,
);

const daysLeft = computed(() => {
  if (!tls.value?.wildcard) return null;
  return Math.floor((tls.value.wildcard.notAfter - Date.now()) / 86_400_000);
});

const certDraft = ref('');
const keyDraft = ref('');
const storing = ref(false);
async function storeWildcard(): Promise<void> {
  storing.value = true;
  const r = await api.put<{ wildcard: TlsWildcardShape; note: string }>('/api/tls/wildcard', {
    cert: certDraft.value,
    key: keyDraft.value,
  });
  storing.value = false;
  if (!r.ok) {
    toast('error', problem(r, 'store the wildcard'));
    return;
  }
  certDraft.value = '';
  keyDraft.value = '';
  toast('ok', 'Stored — redeploy all stacks.');
  redeployed.value = null; // that summary described a run under the previous mode
  await loadTls();
}
type TlsWildcardShape = NonNullable<TlsStatus['wildcard']>;

const removing = ref(false);
async function removeWildcard(): Promise<void> {
  removing.value = true;
  const r = await api.del<{ note: string }>('/api/tls/wildcard');
  removing.value = false;
  if (!r.ok) {
    toast('error', problem(r, 'remove the wildcard'));
    return;
  }
  toast('ok', 'Removed — redeploy all stacks.');
  redeployed.value = null;
  await loadTls();
}

const redeploying = ref(false);
const redeployed = ref<TlsRedeploy | null>(null);
async function redeployAll(): Promise<void> {
  redeploying.value = true;
  const r = await api.post<TlsRedeploy>('/api/tls/redeploy', {});
  redeploying.value = false;
  if (!r.ok) {
    toast('error', problem(r, 'redeploy the stacks'));
    return;
  }
  redeployed.value = r.body;
  toast('ok', `Redeploying ${r.body.started.length}, skipped ${r.body.skipped.length}.`);
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Control stack</h1>
        <!--
          The vitals, not a sentence about them. Restarts and OOM are here rather than buried in the
          table because they are the two numbers that explain a control plane misbehaving — a
          restarting Traefik loses in-flight certificate issuance while every container still reads
          `running`. Why that matters is docs/usage.md's job, not this page's.
        -->
        <div v-if="view?.reachable" class="vitals">
          <span :class="restarts ? 'v-warn' : ''">{{ restarts }} restarts</span>
          <span v-if="oomed" class="v-fail">OOM</span>
          <span v-if="tls">{{ tls.mode }}</span>
          <span v-if="daysLeft !== null" :class="daysLeft < 21 ? 'v-fail' : ''">{{ daysLeft }}d cert</span>
          <span v-if="domainCount">{{ domainCount }} domain{{ domainCount > 1 ? 's' : '' }}</span>
        </div>
      </div>
      <span class="grow" />
      <RefreshButton :run="load" />
    </div>

    <ErrorNote v-if="error" :text="error" title="Could not read the control stack." />

    <SkeletonList v-if="!loaded && !error" :rows="3" />

    <section v-else-if="view" class="panel">
      <div class="phead">
        <h2 class="section">Containers</h2>
      </div>

      <!-- Unknown is not "empty": a dead docker means NOTHING here is known. -->
      <!-- Unknown is not empty: a dead docker means nothing here is KNOWN. The distinction is the
           point (invariant 10); the sentence explaining it is not. -->
      <div v-if="!view.reachable" class="banner warn"><b>Docker isn't answering.</b> Nothing here is known.</div>
      <table v-else-if="view.containers.length" class="cards">
        <thead>
          <tr>
            <th>Service</th>
            <th>Image</th>
            <th>State</th>
            <th>Restarts</th>
            <th>Memory limit</th>
            <th>Started</th>
            <th></th>
          </tr>
        </thead>
        <tbody class="stagger">
          <tr v-for="(c, i) in view.containers" :key="c.id" :style="{ '--i': i }">
            <td class="name" data-label="service">
              {{ c.service || c.name }}
              <span v-if="c.service === 'pstack'" class="badge info" title="the container answering this page">this API</span>
            </td>
            <td data-label="image" class="mono mute">{{ c.image }}</td>
            <td data-label="state">
              <span :class="c.state === 'running' ? 's-ok' : 's-failed'">{{ sentence(c.state) }}</span>
              <span v-if="c.health" class="mute" style="font-size: var(--t-sm)"> · {{ c.health }}</span>
            </td>
            <td data-label="restarts">
              <span :class="c.restartCount > 0 ? 'badge warn' : 'mute'">{{ c.restartCount }}</span>
              <span
                v-if="c.oomKilled"
                class="badge failed"
                title="Killed by the kernel for exceeding its memory limit"
                >OOM</span
              >
            </td>
            <td data-label="memory" class="mono mute">{{ mem(c.memLimitBytes) }}</td>
            <td data-label="started" class="mute">{{ c.startedAt ? ago(c.startedAt) : '—' }}</td>
            <td data-label="">
              <ActionButton
                v-if="c.service && c.service !== 'pstack'"
                variant="ghost"
                :pending="restarting === c.service"
                :disabled="restarting !== ''"
                @click="restart(c.service)"
              >
                Restart
              </ActionButton>
              <span
                v-else-if="c.service === 'pstack'"
                class="mute"
                style="font-size: var(--t-sm)"
                title="Restart this one from the host"
              >
                host-only
              </span>
              <span
                v-else
                class="mute"
                style="font-size: var(--t-sm)"
                title="No compose service label, so the restart API cannot address it."
              >
                —
              </span>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="mute">None listed — the project label changed, not the stack stopped.</p>
    </section>

    <!-- ============================ the hostnames ============================ -->
    <section v-if="domains" class="panel">
      <div class="phead">
        <h2 class="section">Domains</h2>
        <span class="grow" />
        <span class="mute" style="font-size: var(--t-sm)">primary <span class="mono">{{ domains.primary || '—' }}</span></span>
      </div>

      <ul class="kvlist" style="margin: var(--s3) 0">
        <li>
          <span class="k mono">{{ domains.primary || 'not set' }}</span>
          <span class="v mute">primary</span>
        </li>
        <li v-for="d in domains.domains" :key="d">
          <span class="k mono">{{ d }}</span>
          <span class="v">
            <span class="mute">control.{{ d }} · api.{{ d }} · wakes sleeping previews</span>
            <button v-if="can('maintainer')" class="sm ghost" :disabled="savingDomains" @click="removeDomain(d)">Remove</button>
          </span>
        </li>
      </ul>

      <div v-if="can('maintainer')" class="row" style="align-items: flex-end; gap: var(--s3); flex-wrap: wrap">
        <div class="field inline" style="flex: 1 1 18rem">
          <label for="new-domain">Add a domain</label>
          <input
            id="new-domain"
            v-model="newDomain"
            class="mono"
            placeholder="preview.new-company.com"
            spellcheck="false"
            @keyup.enter="newDomain.trim() && addDomain()"
          />
        </div>
        <ActionButton variant="primary" :pending="savingDomains" :disabled="savingDomains || !newDomain.trim()" @click="addDomain">
          Add
        </ActionButton>
      </div>

    </section>

    <!-- ============================ the certificate mode ============================ -->
    <section v-if="tls" class="panel">
      <div class="phead">
        <h2 class="section">Certificates</h2>
        <span class="grow" />
        <span class="badge" :class="tls.mode === 'dns-persist-01' ? 'ok' : 'info'">{{ tls.mode }}</span>
      </div>

      <template v-if="tls.wildcard">
        <ul class="kvlist" style="margin: var(--s3) 0">
          <li>
            <span class="k">Covers</span>
            <span class="v mono">{{ tls.wildcard.domains.join(', ') }}</span>
          </li>
          <li>
            <span class="k">Valid until</span>
            <span class="v">
              {{ stamp(tls.wildcard.notAfter) }}
              <span :class="daysLeft !== null && daysLeft < 21 ? 'badge failed' : 'mute'"> {{ daysLeft }} days left</span>
            </span>
          </li>
          <li>
            <span class="k">Issuer</span>
            <span class="v">
              {{ tls.wildcard.issuer || 'unnamed' }}
              <span v-if="tls.wildcard.selfSigned" class="badge warn">self-signed — browsers will warn</span>
            </span>
          </li>
        </ul>
        <div v-if="can('admin')" class="row">
          <ActionButton
            variant="danger"
            :pending="removing"
            :disabled="removing"
            confirm="Remove it? Deployed stacks lose TLS."
            @run="removeWildcard"
          >
            Remove wildcard
          </ActionButton>
          <span class="mute">Stacks deployed under it serve no certificate until redeployed.</span>
        </div>
      </template>

      <template v-if="can('admin')">
        <p class="dim">Stored 0600. Never returned.</p>
        <div class="field">
          <label for="tls-cert">Certificate (PEM, leaf first, chain after)</label>
          <textarea id="tls-cert" v-model="certDraft" rows="5" class="mono" spellcheck="false" placeholder="-----BEGIN CERTIFICATE-----"></textarea>
        </div>
        <div class="field">
          <label for="tls-key">Private key (PEM)</label>
          <textarea id="tls-key" v-model="keyDraft" rows="4" class="mono" spellcheck="false" placeholder="-----BEGIN PRIVATE KEY-----"></textarea>
        </div>
        <ActionButton variant="primary" :pending="storing" :disabled="storing || !certDraft || !keyDraft" @click="storeWildcard">
          {{ tls.wildcard ? 'Replace wildcard' : 'Store wildcard' }}
        </ActionButton>
      </template>
      <p v-else-if="!tls.wildcard" class="mute">Admin only.</p>

      <div class="row" style="margin-top: var(--s4); align-items: center">
        <ActionButton :pending="redeploying" :disabled="redeploying" @click="redeployAll">
          Redeploy all stacks
        </ActionButton>
        <span class="mute">Router labels are stamped at deploy time. Asleep stacks pick them up on wake.</span>
      </div>
      <p v-if="redeployed" class="mute" style="margin-top: var(--s2)">
        Started {{ redeployed.started.length }} · skipped {{ redeployed.skipped.length
        }}<template v-if="redeployed.skipped.length"> ({{ redeployed.skipped.map((x) => `${x.id}: ${x.reason}`).join('; ') }})</template>
        — watch them under Jobs.
      </p>
    </section>
  </div>
</template>
