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
import HelpModal from '../components/HelpModal.vue';
import InfoHint from '../components/InfoHint.vue';
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
  toast('ok', `Restarting ${r.body.container} — back in a few seconds.`);
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
  toast('ok', 'Saved — Traefik picks these up within a couple of seconds.');
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
  toast('ok', 'Wildcard stored — new deploys inherit it now. Redeploy the rest below.');
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
  toast('ok', 'Removed — back to Traefik-native resolution. Redeploy the stacks below.');
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
  toast('ok', `Redeploying ${r.body.started.length} stack${r.body.started.length === 1 ? '' : 's'}; ${r.body.skipped.length} skipped.`);
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>
          Control stack
          <HelpModal title="Why restarts are the headline here">
            <p>
              <b>A restarting Traefik quietly breaks TLS issuance.</b> Its certificate challenges
              live in process memory, so every restart abandons whatever was mid-flight — the failed
              attempts still count against Let's Encrypt's weekly limits, and every container on
              this page keeps reporting <code>running</code>. A climbing restart count with an
              <b>OOM</b> badge is that story told in two cells.
            </p>
            <p>
              <b>Restart is the only action, and never for pstack.</b> The server refuses to restart
              its own container — it is the process answering the request, and if its image were
              broken, the thing that could repair this host would die with it. From the host:
              <code>docker compose -p pstack-control restart pstack</code>.
            </p>
            <p>
              <b>Restarting traefik drops every connection for a few seconds</b> — every preview,
              this page included. The containers themselves keep running; only routing blinks.
            </p>
          </HelpModal>
        </h1>
        <div class="sub">
          The machinery previews run behind, with the counters that catch it misbehaving
          <InfoHint label="where this comes from">
            <code>docker inspect</code> over the control project's containers, every ten seconds.
            Restart counts and OOM flags are docker's own, since this process last recreated nothing.
          </InfoHint>
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
      <div v-if="!view.reachable" class="banner warn">
        <b>Docker did not answer.</b>
        <p>Nothing about the control stack is known right now — which is not the same as it being down.</p>
      </div>
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
                title="The kernel killed it for exceeding its memory limit. Its last restart was not a crash — it was this."
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
                title="The server refuses to restart the container answering this request. Restart it from the host."
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
      <p v-else class="mute">
        Docker lists no control containers — on a host serving this page, that means the project
        label changed, not that nothing runs.
      </p>
    </section>

    <!-- ============================ the hostnames ============================ -->
    <section v-if="domains" class="panel">
      <div class="phead">
        <h2 class="section">Domains</h2>
        <span class="grow" />
        <span class="mute" style="font-size: var(--t-sm)">primary <span class="mono">{{ domains.primary || '—' }}</span></span>
      </div>

      <p class="dim">{{ domains.note }}</p>

      <ul class="kvlist" style="margin: var(--s3) 0">
        <li>
          <span class="k mono">{{ domains.primary || 'not set' }}</span>
          <span class="v mute">
            primary — its routers are labels on the pstack container, so it keeps working whatever
            else changes here. It cannot be removed from this page.
          </span>
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
      <p class="mute" style="font-size: var(--t-sm); margin-top: var(--s2)">
        Adding a domain only makes this host <b>serve and wake</b> names under it. To move a
        deployment, set <code>PREVIEW_DOMAIN</code> in its variables and redeploy it — one stack at a
        time, and rolling back is that same stack.
      </p>
    </section>

    <!-- ============================ the certificate mode ============================ -->
    <section v-if="tls" class="panel">
      <div class="phead">
        <h2 class="section">Certificates</h2>
        <span class="grow" />
        <span class="badge" :class="tls.mode === 'dns-persist-01' ? 'ok' : 'info'">{{ tls.mode }}</span>
      </div>

      <p class="dim">{{ tls.note }}</p>

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
          <ActionButton variant="danger" :pending="removing" :disabled="removing" @click="removeWildcard">
            Remove wildcard
          </ActionButton>
          <span class="mute" style="font-size: var(--t-sm)">
            Stacks deployed under it then have no certificate until redeployed.
          </span>
        </div>
      </template>

      <template v-if="can('admin')">
        <p class="dim" style="margin-top: var(--s3)">
          <template v-if="tls.wildcard">
            <b>Renew or replace it.</b> Paste the new pair — it overwrites both halves in place and
            Traefik picks it up immediately. Nothing needs redeploying for a renewal: the router
            labels do not change, only the certificate behind them.
          </template>
          <template v-else>
            <b>Bring your own wildcard.</b> Paste a certificate for <span class="mono">*.your-domain</span>
            and its key — previews stop ordering per-hostname certificates the moment it lands, with no
            re-init and no Traefik restart.
          </template>
          The key is stored 0600 and nothing ever returns it.
        </p>
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
      <p v-else-if="!tls.wildcard" class="mute">
        Storing a wildcard is an admin's: it changes what every hostname on this host presents.
      </p>

      <div class="row" style="margin-top: var(--s4); align-items: center">
        <ActionButton :pending="redeploying" :disabled="redeploying" @click="redeployAll">
          Redeploy all stacks
        </ActionButton>
        <span class="mute" style="font-size: var(--t-sm)">
          Router labels are stamped at deploy time — run this once after changing the mode. Asleep
          stacks are skipped; they pick the new labels up when they wake.
        </span>
      </div>
      <p v-if="redeployed" class="mute" style="margin-top: var(--s2)">
        Started {{ redeployed.started.length }} · skipped {{ redeployed.skipped.length
        }}<template v-if="redeployed.skipped.length"> ({{ redeployed.skipped.map((x) => `${x.id}: ${x.reason}`).join('; ') }})</template>
        — watch them under Jobs.
      </p>
    </section>
  </div>
</template>
