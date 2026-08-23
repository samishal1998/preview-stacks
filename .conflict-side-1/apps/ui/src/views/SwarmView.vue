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
import HelpModal from '../components/HelpModal.vue';
import InfoHint from '../components/InfoHint.vue';
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
  { value: 'command', label: 'docker swarm join …', blurb: 'One line, for a machine that already runs Docker.' },
  { value: 'script', label: 'Shell script', blurb: 'Installs Docker if missing (get.docker.com), then joins.' },
  { value: 'cloud-config', label: 'cloud-config', blurb: 'User-data for a fresh cloud machine: Docker from the distro, then join.' },
  { value: 'token', label: 'Token only', blurb: 'The bare join token, for your own tooling.' },
];

async function reveal(): Promise<void> {
  revealing.value = true;
  joinError.value = '';
  revealed.value = '';
  const q = `format=${format.value}${format.value === 'cloud-config' ? `&distro=${encodeURIComponent(distro.value)}` : ''}`;
  const r = await api.getText(`/api/swarm/join?${q}`);
  revealing.value = false;
  if (r.status === 403) {
    joinError.value = 'The join token is admin-only. Sign in as an admin, or use the machine token.';
    return;
  }
  if (r.status === 409) {
    joinError.value = 'This host is not a swarm manager, so there is nothing to join. Nothing was revealed.';
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
    toast('error', 'Could not reach the clipboard — select the text and copy it.');
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
        <h1>
          Swarm
          <HelpModal title="How swarm mode works">
            <p>
              <b>This host is the manager.</b> Previews deploy as swarm stacks — the compose file you
              submit is converted on every deploy (profiles resolved, <code>restart</code> and limits
              moved under <code>deploy</code>, Traefik labels onto the service) — and their tasks run
              on whichever node has room.
            </p>
            <p>
              <b>A worker is one command away.</b> Open the ports below between the machines, run the
              join material on the new one, and it appears in this table. Nothing else on this host
              changes: the control plane stays here, the axes and the leak gate never cared which node
              a container landed on.
            </p>
            <p>
              <b>What a worker cannot do.</b> A shell and stop/start reach only tasks on this node
              (docker's verbs are node-local); logs reach every node through the manager. A volume a
              task creates on a worker stays there when the stack is torn down — use named volumes
              only for data you can lose, and an axis for data you cannot.
            </p>
          </HelpModal>
        </h1>
        <div class="sub">
          The nodes previews run on, and how to add one
          <InfoHint label="where this comes from">
            <code>docker node ls</code> on this host, every ten seconds. Nothing is stored — a node that
            leaves disappears from here the moment docker stops listing it.
          </InfoHint>
        </div>
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
        <b>Docker did not answer.</b>
        <p>Nothing about the swarm is known right now — which is not the same as there being no swarm.</p>
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
        <p v-else class="mute">Docker lists no nodes — on a manager that should at least be this one.</p>
      </template>
    </section>

    <!-- ============================ add a worker ============================ -->
    <section v-if="info?.active" class="panel">
      <div class="phead">
        <h2 class="section">Add a worker</h2>
      </div>

      <p class="dim">
        Open these between the new machine and every other node first — a worker that cannot reach
        them joins and then never receives a task.
      </p>
      <ul class="kvlist" style="margin-bottom: var(--s4)">
        <li v-for="p in info.ports" :key="p.port">
          <span class="k mono">{{ p.port }}</span>
          <span class="v mute">{{ p.why }}</span>
        </li>
      </ul>

      <div class="row" style="flex-wrap: wrap; gap: var(--s3); align-items: flex-end">
        <div class="field inline">
          <label for="join-format">Give me</label>
          <SelectMenu
            id="join-format"
            :model-value="format"
            label="What to reveal"
            :options="FORMATS.map((f) => ({ value: f.value, label: f.label }))"
            @update:model-value="(v) => (format = v as Format)"
          />
        </div>
        <div v-if="format === 'cloud-config'" class="field inline">
          <label for="join-distro">Distro</label>
          <SelectMenu
            id="join-distro"
            v-model="distro"
            label="Which Docker install steps"
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
        <b>This is a secret — whoever has it can add a node that runs any task here.</b>
        <p>
          Shown once; it is not stored in this page and is forgotten when you leave. Rotate it on the
          manager with <code>docker swarm join-token --rotate worker</code> if it leaks.
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
