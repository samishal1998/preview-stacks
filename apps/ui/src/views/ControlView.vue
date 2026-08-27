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
import { ref } from 'vue';
import { api, problem } from '../api/client';
import type { ControlRuntime } from '../api/types';
import { usePolling } from '../composables/usePolling';
import { ago, sentence } from '../composables/useFormat';
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
  </div>
</template>
