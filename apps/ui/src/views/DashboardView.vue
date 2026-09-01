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
import { leakedJobs, loadDeployments, loadJobs, state, summary } from '../composables/useControlPlane';
import { usePolling } from '../composables/usePolling';
import { actionLabel, ago, took } from '../composables/useFormat';
import StateBadge from '../components/StateBadge.vue';
import SkeletonList from '../components/SkeletonList.vue';
import InfoHint from '../components/InfoHint.vue';
import RefreshButton from '../components/RefreshButton.vue';

const control = ref<Control | null>(null);
const controlError = ref('');
const controlLoaded = ref(false);

/**
 * Everything this page shows, in one press.
 *
 * The dashboard is four panels from three sources, so refreshing one of them would leave the others
 * looking current while showing older data — worse than not refreshing at all.
 */
async function refreshAll(): Promise<void> {
  await Promise.all([loadControl(), loadDeployments(), loadJobs()]);
}

async function loadControl(): Promise<void> {
  const r = await api.get<Control>('/api/control');
  controlLoaded.value = true;
  if (!r.ok) {
    controlError.value = problem(r, 'load the control stack');
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
      </div>
      <span class="grow" />
      <RefreshButton :run="refreshAll" />
    </div>

    <!--
      A leak is the single thing this whole tool exists to catch, so it outranks every other panel
      on the page. Nothing else will clean up what survived a teardown.
    -->
    <div v-if="leakedJobs.length" class="banner leaked land">
      <b>{{ leakedJobs.length }} teardown(s) left something behind.</b>
      <p>
        Nothing else will remove it.
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
          <InfoHint label="why there are no actions here">
            pstack runs in this stack, so it is managed from the
            host<template v-if="control?.managedBy"> by {{ control.managedBy }}</template>.
          </InfoHint>
          <span v-if="control?.project" class="badge">{{ control.project }}</span>
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
            <b>Docker did not answer.</b>
            <p>State unknown — the stack may be running.</p>
          </div>
          <div v-else-if="control.parseError" class="banner warn">
            <b>Docker's answer could not be read.</b>
            <p>State unknown for this refresh.</p>
          </div>
          <div v-else-if="!control.services.length" class="banner failed">
            <b>Nothing is running in this project.</b>
          </div>

          <table v-if="control.services.length" class="cards">
            <thead>
              <tr>
                <th>Service</th>
                <th>State</th>
                <th>Health</th>
                <th>Image</th>
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
            NEVER ADD AN ACTION HERE, not even a safe one. The API serving this page runs INSIDE this
            stack: `up` would kill the process performing it, and a failed self-upgrade leaves the
            host with no control plane and no remote way in. The server says `actionable: false` for
            exactly this reason.

            This used to be a banner reading "Read-only — there are no actions here", which spent a
            block of the page announcing the ABSENCE of something. Nobody looks at a panel with no
            buttons and wonders why; the handful who do get the reason from the hint in the header.
          -->
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
            <!-- `data-zero` dims a nought so it stops competing with the counts that moved. -->
            <div class="stats">
              <div class="stat" :data-zero="summary.total === 0"><div class="v">{{ summary.total }}</div><div class="k">total</div></div>
              <div class="stat" :data-zero="summary.isolated === 0"><div class="v">{{ summary.isolated }}</div><div class="k">isolated</div></div>
              <div class="stat" :data-zero="summary.shared === 0"><div class="v">{{ summary.shared }}</div><div class="k">shared</div></div>
              <div class="stat" :data-zero="summary.running === 0"><div class="v">{{ summary.running }}</div><div class="k">running</div></div>
              <div class="stat busy" :data-zero="summary.busy === 0"><div class="v">{{ summary.busy }}</div><div class="k">busy</div></div>
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
              {{ summary.unresolved }} deployment(s) need variables.
              <RouterLink to="/deployments">Set them →</RouterLink>
            </p>
            <p v-else-if="summary.unknown" class="hint">
              Unknown is not zero — state could not be determined.
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
                <RouterLink :to="`/jobs/${encodeURIComponent(j.id)}`">
                  {{ actionLabel(j.action) }} {{ j.stack }}
                </RouterLink>
                <span class="mute" style="font-size: var(--t-xs)">
                  · {{ ago(j.startedAt) }} · {{ took(j.startedAt, j.endedAt) }}
                </span>
              </span>
            </li>
            <li v-if="!recentJobs.length" class="mute">No jobs yet.</li>
          </ul>
        </section>
      </div>
    </div>
  </div>
</template>
