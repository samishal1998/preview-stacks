<script setup lang="ts">
/**
 * Dashboard: control-stack health, deployment counts, recent jobs, activity.
 *
 * The deployment and job lists are already polled by the shell, so this view only fetches the one
 * thing nobody else needs — the control stack.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import type { Control } from '../api/types';
import { leakedJobs, state, summary } from '../composables/useControlPlane';
import { usePolling } from '../composables/usePolling';
import { ago, took } from '../composables/useFormat';
import StateBadge from '../components/StateBadge.vue';
import SkeletonList from '../components/SkeletonList.vue';

const control = ref<Control | null>(null);
const controlError = ref('');
const controlLoaded = ref(false);

async function loadControl(): Promise<void> {
  const r = await api.get<Control>('/api/control');
  controlLoaded.value = true;
  if (!r.ok) {
    controlError.value = problem(r, 'GET /api/control');
    return;
  }
  controlError.value = '';
  const b = r.body;
  control.value = {
    project: b.project ?? '',
    // `reachable !== false`, not `!!reachable`: an older build that omits the field should not be
    // reported as "docker did not answer".
    reachable: b.reachable !== false,
    parseError: !!b.parseError,
    services: Array.isArray(b.services) ? b.services : [],
    managedBy: b.managedBy ?? '',
    actionable: b.actionable === true,
    note: b.note ?? '',
  };
}

usePolling(() => void loadControl(), 10000);

const recentJobs = computed(() => state.jobs.slice(0, 8));
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Dashboard</h1>
        <div class="sub">One host, one Docker socket, one registry.</div>
      </div>
    </div>

    <!--
      A leak is the single thing this whole tool exists to catch, so it outranks every other panel
      on the page. Nothing else will clean up what survived a teardown.
    -->
    <div v-if="leakedJobs.length" class="banner leaked land">
      <b>{{ leakedJobs.length }} job(s) ended LEAKED.</b>
      <p>
        Teardown ran and something survived it. <code>compose down -v</code> removed containers,
        volumes and networks; whatever is left is outside compose's reach and nothing else will
        remove it.
        <RouterLink
          v-for="j in leakedJobs.slice(0, 3)"
          :key="j.id"
          :to="`/jobs/${encodeURIComponent(j.id)}`"
          style="margin-left: 8px"
          >{{ j.stack }} →</RouterLink
        >
      </p>
    </div>

    <div class="grid-2">
      <!-- ============================ control stack ============================ -->
      <section class="panel">
        <div class="phead">
          <h2 class="section">Control stack</h2>
          <span v-if="control?.project" class="badge mono">{{ control.project }}</span>
          <span class="grow" />
          <button class="sm ghost" @click="loadControl">Refresh</button>
        </div>

        <p v-if="controlError" class="s-failed">{{ controlError }}</p>
        <SkeletonList v-else-if="!controlLoaded" :rows="3" />

        <template v-else-if="control">
          <!--
            Three distinct failure shapes. Collapsing any of them into "nothing is running" would be
            a lie an operator acts on.
          -->
          <div v-if="!control.reachable" class="banner warn">
            <b>docker did not answer.</b>
            <p>
              This is <em>not</em> the same as “nothing is running” — the control stack may be
              perfectly healthy while this process cannot reach the socket. Check the daemon and the
              socket mount, then refresh.
            </p>
          </div>
          <div v-else-if="control.parseError" class="banner warn">
            <b>docker answered, but the output could not be parsed.</b>
            <p>Service state is unknown for this refresh; the stack itself may be fine.</p>
          </div>
          <div v-else-if="!control.services.length" class="banner failed">
            <b>no containers in this project.</b>
            <p>
              docker answered clearly, so the control stack really is down — yet you are reading a
              page it serves. You are most likely reaching the API by some other route.
            </p>
          </div>

          <table v-if="control.services.length" class="cards">
            <thead>
              <tr>
                <th>service</th>
                <th>state</th>
                <th>health</th>
                <th>image</th>
              </tr>
            </thead>
            <tbody class="stagger">
              <tr v-for="(s, i) in control.services" :key="s.name" :style="{ '--i': i }">
                <td class="name" data-label="service">{{ s.name }}</td>
                <td data-label="state" :class="s.state === 'running' ? 's-ok' : 's-failed'">
                  {{ s.state || '—' }}
                </td>
                <td
                  data-label="health"
                  :class="s.health === 'healthy' ? 's-ok' : s.health ? 's-failed' : ''"
                >
                  {{ s.health || '—' }}
                </td>
                <td class="name dim" data-label="image">{{ s.image || '—' }}</td>
              </tr>
            </tbody>
          </table>

          <!--
            THIS SITS WHERE AN OPERATOR LOOKS FOR up/down/verify BUTTONS, because the answer to
            "where are they" is the important part. The API serving this page runs INSIDE this
            stack: `up` here would kill the process performing it, and a failed self-upgrade leaves
            the host with no control plane and no remote way in. The server states
            `actionable: false` for exactly this reason. Never add an action here, not even a safe
            one.
          -->
          <div class="banner plain">
            <b>Read-only — there are no actions here.</b>
            <p>
              {{
                control.note ||
                'The control stack is not managed through this API: the process serving this request runs inside it.'
              }}
            </p>
            <p v-if="control.managedBy" class="mute">
              Managed by <code>{{ control.managedBy }}</code
              >.
            </p>
          </div>
        </template>
      </section>

      <!-- ============================ deployments + jobs ============================ -->
      <div>
        <section class="panel">
          <div class="phead">
            <h2 class="section">Deployments</h2>
            <span class="grow" />
            <RouterLink to="/deployments">all →</RouterLink>
          </div>

          <p v-if="state.deploymentsError" class="s-failed">{{ state.deploymentsError }}</p>
          <SkeletonList v-else-if="!state.deploymentsLoaded" :rows="2" tall />
          <template v-else>
            <div class="stats">
              <div class="stat"><div class="v">{{ summary.total }}</div><div class="k">total</div></div>
              <div class="stat"><div class="v">{{ summary.isolated }}</div><div class="k">isolated</div></div>
              <div class="stat"><div class="v">{{ summary.shared }}</div><div class="k">shared</div></div>
              <div class="stat"><div class="v">{{ summary.running }}</div><div class="k">running</div></div>
              <div class="stat busy"><div class="v">{{ summary.busy }}</div><div class="k">busy</div></div>
              <!--
                The server sends `null`, never `false`, when it could not determine busy/running.
                An unknown must never be counted as a zero — that is how a live stack gets reported
                as torn down. It gets its own tile.
              -->
              <div v-if="summary.unknown" class="stat unknown">
                <div class="v">{{ summary.unknown }}</div>
                <div class="k">unknown</div>
              </div>
            </div>

            <p v-if="summary.unresolved" class="hint">
              {{ summary.unresolved }} deployment(s) could not be resolved in this listing — their
              spec needs <code>${VAR}</code> values.
              <RouterLink to="/deployments">Open one and set its variables.</RouterLink>
            </p>
            <p v-else-if="summary.unknown" class="hint">
              “unknown” means the server could not determine <code>busy</code> or
              <code>running</code> for that row, <em>not</em> that it is idle.
            </p>
          </template>
        </section>

        <section class="panel">
          <div class="phead">
            <h2 class="section">Recent jobs</h2>
            <span class="grow" />
            <RouterLink to="/jobs">all →</RouterLink>
          </div>

          <p v-if="state.jobsError" class="s-failed">{{ state.jobsError }}</p>
          <SkeletonList v-else-if="!state.jobsLoaded" :rows="4" />
          <ul v-else class="kvlist stagger">
            <li v-for="(j, i) in recentJobs" :key="j.id" :style="{ '--i': i }">
              <span class="k"><StateBadge :state="j.state" /></span>
              <span class="v">
                <RouterLink :to="`/jobs/${encodeURIComponent(j.id)}`" class="mono">
                  {{ j.action }} {{ j.stack }}
                </RouterLink>
                <span class="mute" style="font-size: var(--t-xs)">
                  · {{ ago(j.startedAt) }} · {{ took(j.startedAt, j.endedAt) }}
                </span>
              </span>
            </li>
            <li v-if="!recentJobs.length" class="mute">
              No jobs yet — job history is held in memory, so a server restart clears it. A job
              record is the transcript of an attempt, not the truth about what exists.
            </li>
          </ul>
        </section>
      </div>
    </div>
  </div>
</template>
