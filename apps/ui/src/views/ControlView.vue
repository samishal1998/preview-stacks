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
import { computed, ref, watch } from 'vue';
import { api, problem } from '../api/client';
import type {
  ControlRuntime,
  DomainsStatus,
  JobResponse,
  JobStub,
  LokiSettings,
  LokiStorageInput,
  TlsRedeploy,
  TlsStatus,
} from '../api/types';
import { usePolling } from '../composables/usePolling';
import { can } from '../composables/useAuth';
import { state } from '../composables/useControlPlane';
import { ago, sentence, stamp } from '../composables/useFormat';
import { isTerminal, supersededBy } from '../composables/useJobQueue';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import RefreshButton from '../components/RefreshButton.vue';
import SelectMenu from '../components/SelectMenu.vue';
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

// ── Loki's settings ───────────────────────────────────────────────────────────────────────────────
const logging = ref<LokiSettings | null>(null);
const loggingError = ref('');
const savingLogging = ref(false);
/** The apply job a save here started — or the successor carrying it — until it ends. */
const applyingJob = ref<string | null>(null);

/** `type="number"` hands back a number once the box parses and '' while it is empty (useJobQueue.ts:64-71). */
type Box = number | string;
const draft = ref<{ retentionDays: Box; idlePeriodMinutes: Box; maxAgeMinutes: Box; targetSizeKiB: Box; encoding: string }>({
  retentionDays: '',
  idlePeriodMinutes: '',
  maxAgeMinutes: '',
  targetSizeKiB: '',
  encoding: '',
});
const storageType = ref<'filesystem' | 's3'>('filesystem');
const s3Draft = ref({ endpoint: '', region: '', bucket: '', pathStyle: false, accessKeyId: '' });
const secretDraft = ref('');

// Refill only when the SAVED values change (a rollback included): the 10s poll must not wipe typing.
watch(
  () => logging.value && JSON.stringify([logging.value.retentionDays, logging.value.chunks, logging.value.storage]),
  () => {
    const l = logging.value;
    if (!l) return;
    draft.value = { retentionDays: l.retentionDays, ...l.chunks };
    storageType.value = l.storage.type;
    const s3 = l.storage.s3;
    s3Draft.value = {
      endpoint: s3?.endpoint ?? '',
      region: s3?.region ?? '',
      bucket: s3?.bucket ?? '',
      pathStyle: s3?.pathStyle ?? false,
      accessKeyId: s3?.accessKeyId ?? '',
    };
  },
);

const onS3 = computed(() => logging.value?.storage.type === 's3');
/** S3 is one-way and fixed once saved; below admin nothing in storage is writable. */
const storageLocked = computed(() => onS3.value || !can('admin'));
const encodings = computed(() => (logging.value?.limits.encodings ?? []).map((e) => ({ value: e, label: e })));
const storageBadge = computed(() => {
  const s3 = logging.value?.storage.s3;
  if (!s3) return 'Filesystem';
  // The cutover is 00:00 UTC of its day; before it Loki still writes to the filesystem.
  return s3.cutover > new Date().toISOString().slice(0, 10) ? `S3 from ${s3.cutover}` : 'S3';
});

/** One read of the followed job: keep following, follow its successor, or end with a toast. */
async function follow(id: string): Promise<void> {
  const r = await api.get<JobResponse>(`/api/jobs/${encodeURIComponent(id)}`);
  if (applyingJob.value !== id) return; // a newer save took over during the read
  if (r.status === 404) {
    applyingJob.value = null; // the record is gone
    return;
  }
  if (!r.ok || !isTerminal(r.body.job.state)) return;
  const job = r.body.job;
  if (job.state === 'superseded') {
    // No toast: the successor carries this save. Keep this id until the shell's job list shows it.
    applyingJob.value = supersededBy(job, state.jobs)?.id ?? id;
    return;
  }
  applyingJob.value = null;
  const to = { to: `/jobs/${encodeURIComponent(id)}` };
  if (job.state === 'ok') toast('ok', 'Applied.', to);
  else if (job.state === 'cancelled') toast('warn', 'Cancelled.', to);
  else toast('error', 'Apply failed.', to); // `leaked` cannot happen: an apply has no assert_gone
}

async function loadLogging(): Promise<void> {
  if (!can('maintainer')) return; // the read is maintainer's; below it every tick would 403
  if (applyingJob.value) await follow(applyingJob.value);
  const r = await api.get<LokiSettings>('/api/logging');
  if (r.ok) logging.value = r.body;
}
usePolling(loadLogging, 10_000);

async function saveLogging(path: string, body: unknown): Promise<void> {
  savingLogging.value = true;
  const r = await api.put<{ job: JobStub } | { changed: false }>(path, body);
  savingLogging.value = false;
  if (!r.ok) {
    loggingError.value = r.body.error ?? `HTTP ${r.status}`;
    return;
  }
  loggingError.value = '';
  secretDraft.value = '';
  const res = r.body;
  if ('job' in res) {
    applyingJob.value = res.job.id;
    toast('info', 'Applying.', { to: `/jobs/${encodeURIComponent(res.job.id)}`, toLabel: 'Follow' });
  } else {
    toast('ok', 'No change.');
  }
  await loadLogging();
}

const saveChunks = () =>
  saveLogging('/api/logging', {
    retentionDays: Number(draft.value.retentionDays),
    chunks: {
      idlePeriodMinutes: Number(draft.value.idlePeriodMinutes),
      maxAgeMinutes: Number(draft.value.maxAgeMinutes),
      targetSizeKiB: Number(draft.value.targetSizeKiB),
      encoding: draft.value.encoding,
    },
  });

async function saveStorage(): Promise<void> {
  const l = logging.value;
  if (!l) return;
  const body: LokiStorageInput = {
    type: 's3',
    ...s3Draft.value,
    secretAccessKey: secretDraft.value, // '' keeps the stored secret
    cutover: l.storage.s3?.cutover ?? l.limits.earliestCutover, // the date the confirm named
  };
  await saveLogging('/api/logging/storage', body);
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
      <table v-else-if="view.containers.length" role="table" class="cards">
        <thead role="rowgroup">
          <tr role="row">
            <th role="columnheader">Service</th>
            <th role="columnheader">Image</th>
            <th role="columnheader">State</th>
            <th role="columnheader">Restarts</th>
            <th role="columnheader">Memory limit</th>
            <th role="columnheader">Started</th>
            <th role="columnheader"></th>
          </tr>
        </thead>
        <tbody role="rowgroup" class="stagger">
          <tr v-for="(c, i) in view.containers" :key="c.id" role="row" :style="{ '--i': i }">
            <td role="cell" class="name" data-label="service">
              {{ c.service || c.name }}
              <span v-if="c.service === 'pstack'" class="badge info" title="the container answering this page">This API</span>
            </td>
            <td role="cell" data-label="image" class="mono mute">{{ c.image }}</td>
            <td role="cell" data-label="state">
              <span :class="c.state === 'running' ? 's-ok' : 's-failed'">{{ sentence(c.state) }}</span>
              <span v-if="c.health" class="mute" style="font-size: var(--t-sm)"> · {{ c.health }}</span>
            </td>
            <td role="cell" data-label="restarts">
              <span :class="c.restartCount > 0 ? 'badge warn' : 'mute'">{{ c.restartCount }}</span>
              <span
                v-if="c.oomKilled"
                class="badge failed"
                title="Killed by the kernel for exceeding its memory limit"
                >OOM</span
              >
            </td>
            <td role="cell" data-label="memory" class="mono mute">{{ mem(c.memLimitBytes) }}</td>
            <td role="cell" data-label="started" class="mute">{{ c.startedAt ? ago(c.startedAt) : '—' }}</td>
            <td role="cell" data-label="">
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

      <div v-if="can('maintainer')" class="row" style="gap: var(--s3); flex-wrap: wrap">
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

    <!-- ============================ Loki's settings ============================ -->
    <!-- Hidden when this host runs no Loki. The body is what was saved; the apply job says what ran. -->
    <section v-if="can('maintainer') && logging && logging.enabled !== false" class="panel settings-form">
      <div class="phead">
        <h2 class="section">Logging</h2>
        <a
          class="hint-btn"
          href="https://github.com/samishal1998/preview-stacks/blob/main/docs/usage.md#loki-settings"
          target="_blank"
          rel="noreferrer"
          aria-label="Help"
          >?</a
        >
        <span class="grow" />
        <RouterLink v-if="applyingJob" class="badge running" :to="`/jobs/${encodeURIComponent(applyingJob)}`">Applying</RouterLink>
        <span class="badge info">{{ storageBadge }}</span>
      </div>

      <p v-if="logging.enabled === null" class="mute">Docker did not answer.</p>
      <template v-else>
        <ErrorNote v-if="loggingError" :text="loggingError" />

        <div class="field">
          <label for="loki-retention">Retention</label>
          <div class="row">
            <input
              id="loki-retention"
              v-model="draft.retentionDays"
              type="number"
              step="1"
              inputmode="numeric"
              :min="logging.limits.retentionDays.min"
              :max="logging.limits.retentionDays.max"
              style="width: 8rem"
            />
            <span class="mute">days</span>
          </div>
        </div>
        <div class="field">
          <label for="loki-idle">Chunk idle</label>
          <div class="row">
            <input
              id="loki-idle"
              v-model="draft.idlePeriodMinutes"
              type="number"
              step="1"
              inputmode="numeric"
              :min="logging.limits.idlePeriodMinutes.min"
              :max="logging.limits.idlePeriodMinutes.max"
              style="width: 8rem"
            />
            <span class="mute">min</span>
          </div>
        </div>
        <div class="field">
          <label for="loki-max-age">Chunk max age</label>
          <div class="row">
            <input
              id="loki-max-age"
              v-model="draft.maxAgeMinutes"
              type="number"
              step="1"
              inputmode="numeric"
              :min="logging.limits.maxAgeMinutes.min"
              :max="logging.limits.maxAgeMinutes.max"
              style="width: 8rem"
            />
            <span class="mute">min</span>
          </div>
        </div>
        <div class="field">
          <label for="loki-size">Chunk size</label>
          <div class="row">
            <input
              id="loki-size"
              v-model="draft.targetSizeKiB"
              type="number"
              step="1"
              inputmode="numeric"
              :min="logging.limits.targetSizeKiB.min"
              :max="logging.limits.targetSizeKiB.max"
              style="width: 8rem"
            />
            <span class="mute">KiB</span>
          </div>
        </div>
        <div class="field">
          <label for="loki-encoding">Encoding</label>
          <SelectMenu id="loki-encoding" v-model="draft.encoding" label="Encoding" :options="encodings" />
        </div>
        <div class="row" style="margin-top: var(--s4)">
          <ActionButton variant="primary" :pending="savingLogging" @click="saveChunks">Save</ActionButton>
          <span class="mute">Restarts Loki.</span>
        </div>

        <!-- Toggles, not a tablist (ui-rules "No ARIA is better than bad ARIA"). -->
        <div class="row" role="group" aria-label="Storage" style="margin-top: var(--s5)">
          <button :aria-pressed="storageType === 'filesystem'" :disabled="storageLocked" @click="storageType = 'filesystem'">
            Filesystem
          </button>
          <button :aria-pressed="storageType === 's3'" :disabled="storageLocked" @click="storageType = 's3'">S3</button>
        </div>
        <!-- The reason sits beside the disabled controls as text: a disabled control leaves the tab order. -->
        <p v-if="storageLocked" class="hint">{{ can('admin') ? 'Fixed once saved.' : 'Admin only.' }}</p>

        <template v-if="storageType === 's3'">
          <div class="field">
            <label for="loki-endpoint">Endpoint</label>
            <input id="loki-endpoint" v-model="s3Draft.endpoint" type="url" class="mono" spellcheck="false" :disabled="storageLocked" />
          </div>
          <div class="field">
            <label for="loki-region">Region</label>
            <input id="loki-region" v-model="s3Draft.region" type="text" class="mono" spellcheck="false" :disabled="storageLocked" />
          </div>
          <div class="field">
            <label for="loki-bucket">Bucket</label>
            <input id="loki-bucket" v-model="s3Draft.bucket" type="text" class="mono" spellcheck="false" :disabled="storageLocked" />
          </div>
          <div class="field">
            <label class="check"><input v-model="s3Draft.pathStyle" type="checkbox" :disabled="storageLocked" /> Path-style</label>
          </div>
          <div class="field">
            <label for="loki-key-id">Access key ID</label>
            <input
              id="loki-key-id"
              v-model="s3Draft.accessKeyId"
              type="text"
              class="mono"
              spellcheck="false"
              autocomplete="off"
              :disabled="!can('admin')"
            />
          </div>
          <template v-if="can('admin')">
            <div class="field">
              <label for="loki-secret">Secret access key</label>
              <input id="loki-secret" v-model="secretDraft" type="password" spellcheck="false" autocomplete="new-password" />
              <p class="hint">Write-only.{{ logging.storage.s3?.secretSet ? ' Leave empty to keep.' : '' }}</p>
            </div>
            <!-- Two buttons, not one with a conditional `confirm`: with `confirm` set, a parent @click
                 still fires on the arming click (ActionButton.vue:71). -->
            <div class="row" style="margin-top: var(--s4)">
              <ActionButton v-if="onS3" :pending="savingLogging" @click="saveStorage">Save keys</ActionButton>
              <ActionButton
                v-else
                :pending="savingLogging"
                :confirm="`One-way. S3 from ${logging.limits.earliestCutover} UTC?`"
                @run="saveStorage"
              >
                Switch to S3
              </ActionButton>
            </div>
          </template>
        </template>
      </template>
    </section>
  </div>
</template>

<style scoped>
/* A pressed toggle. app.css styles one only inside EquivalentCommand's own scope (:131). */
button[aria-pressed='true'] {
  background: var(--accent-soft);
  color: var(--accent);
}
/* app.css has no rule for a disabled text input (SettingsView.vue:309-315). */
input:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
</style>
