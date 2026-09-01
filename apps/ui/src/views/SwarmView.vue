<script setup lang="ts">
/**
 * The swarm this host manages: its nodes, and what a new worker needs to join.
 *
 * Read straight from docker on every refresh — there is no cached node list anywhere (a table of
 * what is running is exactly what AGENTS.md invariant 10 refuses). Three states are kept apart:
 * docker did not answer (nothing is known), this daemon is not a manager (previews run with
 * compose), and active (the node table).
 *
 * THE JOIN TOKEN IS A SECRET. Whoever holds it can add a node that runs any task on the cluster.
 * It is fetched on a click and never by the poll, shown once, held in component state only, and
 * cleared the moment this page is left.
 */
import { onBeforeUnmount, ref } from 'vue';
import { api, problem } from '../api/client';
import type { SwarmInfo } from '../api/types';
import { usePolling } from '../composables/usePolling';
import { sentence } from '../composables/useFormat';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import RefreshButton from '../components/RefreshButton.vue';
import SelectMenu from '../components/SelectMenu.vue';
import SkeletonList from '../components/SkeletonList.vue';

const info = ref<SwarmInfo | null>(null);
const error = ref('');
const loaded = ref(false);

async function load(): Promise<void> {
  const r = await api.get<SwarmInfo>('/api/swarm');
  loaded.value = true;
  if (!r.ok) {
    error.value = problem(r, 'read the swarm');
    return;
  }
  error.value = '';
  info.value = {
    ...r.body,
    nodes: r.body.nodes ?? [],
    ports: r.body.ports ?? [],
  };
}
usePolling(load, 10_000);

// ── joining ─────────────────────────────────────────────────────────────────────────────────────
type Format = 'token' | 'command' | 'script' | 'cloud-config';
const format = ref<Format>('command');
const distro = ref('ubuntu');
const revealed = ref('');
const revealing = ref(false);
const joinError = ref('');

const FORMATS: Array<{ value: Format; label: string; blurb: string }> = [
  { value: 'command', label: 'docker swarm join …', blurb: 'For a machine already running Docker.' },
  { value: 'script', label: 'Shell script', blurb: 'Installs Docker, then joins.' },
  { value: 'cloud-config', label: 'cloud-config', blurb: 'User-data for a fresh machine.' },
  { value: 'token', label: 'Token only', blurb: 'For your own tooling.' },
];

async function reveal(): Promise<void> {
  revealing.value = true;
  joinError.value = '';
  revealed.value = '';
  const q = `format=${format.value}${format.value === 'cloud-config' ? `&distro=${encodeURIComponent(distro.value)}` : ''}`;
  const r = await api.getText(`/api/swarm/join?${q}`);
  revealing.value = false;
  if (r.status === 403) {
    joinError.value = 'The join token is admin-only.';
    return;
  }
  if (r.status === 409) {
    joinError.value = 'This host is not a swarm manager.';
    return;
  }
  if (!r.ok) {
    joinError.value = r.text || `HTTP ${r.status}`;
    return;
  }
  revealed.value = r.text;
}

async function copy(): Promise<void> {
  try {
    await navigator.clipboard.writeText(revealed.value);
    toast('ok', 'Copied.');
  } catch {
    toast('error', 'Could not copy.');
  }
}

// Leaving the page forgets the secret; coming back means asking for it again.
onBeforeUnmount(() => {
  revealed.value = '';
});
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <!-- How swarm mode works, what a worker can and cannot do, and where a worker's volumes
             live is docs/usage.md's job; this page shows the cluster as it is right now. -->
        <h1>Swarm</h1>
        <div class="sub">The nodes previews run on</div>
      </div>
      <span class="grow" />
      <RefreshButton :run="load" />
    </div>

    <ErrorNote v-if="error" :text="error" title="Could not read the swarm." />

    <SkeletonList v-if="!loaded && !error" :rows="4" />

    <!-- ============================ the cluster ============================ -->
    <section v-else-if="info" class="panel">
      <div class="phead">
        <h2 class="section">Nodes</h2>
        <span class="grow" />
        <span v-if="info.active && info.managerAddr" class="mute" style="font-size: var(--t-sm)">
          manager <span class="mono">{{ info.managerAddr }}</span>
        </span>
      </div>

      <!-- Three states, never folded: unknown is not "no nodes", and not-a-manager is not an error. -->
      <div v-if="!info.reachable" class="banner warn">
        <b>Docker did not answer — this is not the same as no swarm.</b>
      </div>
      <div v-else-if="!info.active" class="banner plain">
        <b>Not a swarm manager.</b>
        <p>{{ info.note }}</p>
      </div>
      <template v-else>
        <div v-if="info.error" class="banner warn">
          <b>Docker reported a problem.</b>
          <p class="mono">{{ info.error }}</p>
        </div>
        <table v-if="info.nodes.length" class="cards">
          <thead>
            <tr>
              <th>Hostname</th>
              <th>Role</th>
              <th>Status</th>
              <th>Availability</th>
              <th>Engine</th>
              <th>Id</th>
            </tr>
          </thead>
          <tbody class="stagger">
            <tr v-for="(n, i) in info.nodes" :key="n.id" :style="{ '--i': i }">
              <td class="name" data-label="hostname">
                {{ n.hostname || '—' }}
                <span v-if="n.self" class="badge info" title="the node this control plane runs on">this node</span>
              </td>
              <td data-label="role">
                <span class="badge" :class="n.role === 'manager' ? 'isolated' : ''">{{ sentence(n.role) }}</span>
                <span v-if="n.managerStatus" class="mute" style="font-size: var(--t-sm)"> {{ n.managerStatus }}</span>
              </td>
              <td data-label="status">
                <span :class="n.status === 'ready' ? 's-ok' : 's-failed'">{{ sentence(n.status) }}</span>
              </td>
              <td data-label="availability">{{ sentence(n.availability || 'unknown') }}</td>
              <td data-label="engine" class="mono">{{ n.engineVersion || '—' }}</td>
              <td data-label="id" class="mono mute">{{ n.id.slice(0, 12) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="mute">Docker lists no nodes.</p>
      </template>
    </section>

    <!-- ============================ add a worker ============================ -->
    <section v-if="info?.active" class="panel">
      <div class="phead">
        <h2 class="section">Add a worker</h2>
      </div>

      <p class="dim">Open these between the new machine and every other node.</p>
      <ul class="kvlist" style="margin-bottom: var(--s4)">
        <li v-for="p in info.ports" :key="p.port">
          <span class="k mono">{{ p.port }}</span>
          <span class="v mute">{{ p.why }}</span>
        </li>
      </ul>

      <div class="row" style="flex-wrap: wrap; gap: var(--s3); align-items: flex-end">
        <div class="field inline">
          <label for="join-format">Format</label>
          <SelectMenu
            id="join-format"
            :model-value="format"
            label="Join format"
            :options="FORMATS.map((f) => ({ value: f.value, label: f.label }))"
            @update:model-value="(v) => (format = v as Format)"
          />
        </div>
        <div v-if="format === 'cloud-config'" class="field inline">
          <label for="join-distro">Distro</label>
          <SelectMenu
            id="join-distro"
            v-model="distro"
            label="Distro"
            :options="['ubuntu', 'debian', 'fedora', 'suse', 'arch', 'alpine'].map((d) => ({ value: d, label: d }))"
          />
        </div>
        <ActionButton variant="primary" :pending="revealing" :disabled="revealing" @click="reveal">
          Reveal
        </ActionButton>
        <span class="mute" style="font-size: var(--t-sm)">{{ FORMATS.find((f) => f.value === format)?.blurb }}</span>
      </div>

      <ErrorNote v-if="joinError" :text="joinError" title="Nothing was revealed." />

      <div v-if="revealed" class="banner leaked" style="margin-top: var(--s3)">
        <b>Secret, shown once — whoever has it can add a node that runs any task here.</b>
        <p>
          If it leaks, rotate it on the manager:
          <code>docker swarm join-token --rotate worker</code>
        </p>
        <pre class="code" style="white-space: pre-wrap; word-break: break-all; max-height: 50vh; overflow: auto">{{ revealed }}</pre>
        <div class="row">
          <button @click="copy">Copy</button>
          <button @click="revealed = ''">Hide</button>
        </div>
      </div>
    </section>
  </div>
</template>
