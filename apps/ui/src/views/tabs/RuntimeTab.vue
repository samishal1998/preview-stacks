<script setup lang="ts">
/**
 * What is actually running, and what Traefik was actually told about it.
 *
 * THE QUESTION THIS ANSWERS. "The hostname does not work." Nothing in this UI could answer it: the
 * registry shows what was *submitted*, the logs show what the container *says*, and the reason a
 * request 404s is in neither — it is in the container's Traefik labels, which pstack never writes.
 *
 * The routing table is the direct answer to "which URL maps to which port". Traefik does **not** dial
 * `service_name:port`: its docker provider resolves the container's IP on the ingress network and
 * dials that, on the port from the `loadbalancer.server.port` label. That is why a service must be
 * attached to `preview-ingress`, why the port is the *container-internal* one, and why publishing a
 * host port is unnecessary. The `target` column shows the address Traefik assembled, so a missing
 * half is visible rather than inferred — and when there is no address, `RouteTarget` says which of
 * three reasons it is. "Not on the ingress network" was being printed over swarm services whose
 * network was fine, because the address is simply not knowable from a manager.
 *
 * FINDINGS ARE A BUTTON, NOT BANNERS. They used to lead the tab as a stack of banners: fine at one
 * finding, and at nine they pushed both tables off the screen — on the page whose whole job is those
 * tables. The count and the worst level ride on the trigger so a warning still announces itself
 * unopened (see `FindingsModal`). State that changes what you can do — "Docker did not answer",
 * "nothing looks wrong" — stays on the page, because it is one line and cannot grow.
 *
 * BOTH TABLES ARE FIXED-LAYOUT, INSIDE A SCROLLER. Under auto layout a single long image or task
 * name took width from every other column on the row, and `.panel > table` splits thead and tbody
 * into two table boxes that size their columns independently — which is why the headers never lined
 * up with the cells under them. The `<colgroup>`s below are those widths, stated once, beside the
 * headers they have to agree with; wrapping the table in `.table-scroll` is what opts it out of the
 * split and lets it scroll instead of squeezing.
 */
import { computed, ref, watch } from 'vue';
import { sentence } from '../../composables/useFormat';
import { api, problem } from '../../api/client';
import type { RuntimeContainer, RuntimeResponse } from '../../api/types';
import { dep, varsQuery } from '../../composables/useDeployment';
import { usePolling } from '../../composables/usePolling';
import SkeletonList from '../../components/SkeletonList.vue';
import ActionButton from '../../components/ActionButton.vue';
import ErrorNote from '../../components/ErrorNote.vue';
import FindingsModal from '../../components/FindingsModal.vue';
import InfoHint from '../../components/InfoHint.vue';
import RouteTarget from '../../components/RouteTarget.vue';
import { toast } from '../../composables/useToasts';
import RefreshButton from '../../components/RefreshButton.vue';

const rt = ref<RuntimeResponse | null>(null);
const error = ref('');
const loading = ref(true);

async function load(): Promise<void> {
  const r = await api.get<RuntimeResponse>(
    `/api/deployments/${encodeURIComponent(dep.id)}/runtime${varsQuery.value}`,
  );
  loading.value = false;
  if (!r.ok) {
    error.value = problem(r, 'inspect what is running');
    return;
  }
  error.value = '';
  rt.value = r.body;
}

watch(() => dep.id, () => { loading.value = true; void load(); }, { immediate: true });
// Containers change state during a deploy; the poll pauses itself when the tab is hidden.
usePolling(() => void load(), 8000);

const running = computed(() => rt.value?.containers.filter((c) => c.state === 'running').length ?? 0);

/**
 * Whether the node column exists at all — compose has no nodes to name.
 *
 * Hoisted rather than asked twice: the header and the cell were computing this separately, which is
 * how a header can end up describing a column the body did not render in the same position.
 */
const hasNode = computed(() => rt.value?.containers.some((c) => c.node) ?? false);


/**
 * The part of a container's name worth reading in a table cell.
 *
 * Compose names a container `<stack>-<service>-<n>`. Swarm names a TASK
 * `<stack>_<service>.<slot>.<task id>` — long, and its first half repeats the service already
 * printed on the line above, in a column that under swarm has to share the row with the node and
 * the row actions. The tail is the half that identifies which task this is: the slot and the id
 * `docker service ps` prints. Compose names hold no dots and come back untouched.
 *
 * The full name stays in the cell's `title` rather than in the text. Truncating the text would have
 * cut the id — the one part worth copying — and nothing here needs the whole name by hand anyway:
 * Logs, Shell and the container actions all carry it themselves.
 */
function taskOf(name: string): string {
  const p = name.split('.');
  return p.length === 3 ? `${p[1]}.${p[2]}` : name;
}

/**
 * One container action in flight, as `action:name`.
 *
 * A single ref rather than a per-row flag: two `docker restart`s racing on the same stack is not
 * something to make easy, and the readiness watch each one starts would fight the other.
 */
const busy = ref('');

async function act(c: RuntimeContainer, action: 'start' | 'stop' | 'restart'): Promise<void> {
  busy.value = `${action}:${c.name}`;
  const r = await api.post<{ note?: string }>(
    `/api/deployments/${encodeURIComponent(dep.id)}/containers/${encodeURIComponent(c.name)}/${action}${varsQuery.value}`,
  );
  busy.value = '';
  if (!r.ok) {
    // Docker's own words. "Already started" and "no such container" are different problems and it
    // says both better than this layer could.
    toast('error', problem(r, `${action} ${c.name}`));
    return;
  }
  toast('ok', r.body.note ?? `${c.name}: ${action} done.`);
  // Read the table back rather than assuming the new state: a container that exits again immediately
  // is exactly the case worth seeing, and the 8s poll would show it a beat later anyway.
  void load();
}

/**
 * The hostname Traefik actually routes to this container, or null.
 *
 * Built from the ROUTES rather than guessed from the container: a router's rule is the only thing
 * that decides what URL reaches it, and a link assembled from a service name and a domain would be
 * a plausible-looking 404. Pattern hosts (`HostRegexp`) are skipped for the same reason — there is
 * no single address to send someone to.
 */
function urlFor(c: RuntimeContainer): string | null {
  const host = rt.value?.routes
    // Under swarm the labels live on the service, so a route names `app` where the container is a
    // task called `pr-1_app.1.<id>`. Matching the name alone left every swarm row without a link to
    // a router that does point at it. Both tasks of one service share the URL — Traefik balances.
    .filter((r) => r.container === c.name || r.container === c.service)
    .flatMap((r) => r.hosts)
    .find((h) => !h.startsWith('(pattern)'));
  return host ? `https://${host}` : null;
}
</script>

<template>
  <div>
    <ErrorNote v-if="error" :text="error" title="Could not inspect this deployment." />
    <SkeletonList v-if="loading && !rt" :rows="5" />

    <template v-else-if="rt">
      <!--
        `reachable: false` is not "nothing is running". The stack may be perfectly healthy while this
        process cannot reach the Docker socket, and reporting that as empty is how a UI tells someone
        their live preview is gone.
      -->
      <div v-if="!rt.reachable" class="banner warn">
        <b>Docker did not answer — this is not the same as nothing running.</b>
      </div>

      <template v-else>
        <!--
          Findings first — as one control, not as their own page. The button is absent when there is
          nothing to say, so the row disappears with it rather than leaving an empty gap.
        -->
        <div v-if="rt.findings.length" class="row" style="margin: var(--s3) 0">
          <FindingsModal :findings="rt.findings" />
        </div>
        <div v-if="!rt.findings.length && rt.containers.length" class="banner ok">
          <b>Nothing looks wrong.</b>
        </div>

        <!-- ============================ routing ============================ -->
        <section class="panel">
          <div class="phead">
            <h2 class="section">Routing</h2>
            <span class="grow" />
            <span class="mute" style="font-size: var(--t-sm)">
              TLS: {{ rt.challenge === 'unknown' ? 'unknown' : rt.challenge }}
              <!-- The mode itself is a concept docs/tls-challenge.md owns; this only says where
                   it is changed. -->
              <InfoHint label="where to change the TLS mode" align="end">
                Change it on the <RouterLink to="/control">Control stack</RouterLink> page.
              </InfoHint>
            </span>
          </div>

          <div v-if="rt.routes.length" class="table-scroll">
            <table class="cards tbl-fixed t-routes">
              <!--
                The widths, as percentages of whatever the panel gives us. URL is the widest because
                it is the value people read, and it WRAPS: a hostname's tail is which PR it is.
              -->
              <colgroup>
                <col style="width: 34%" />
                <col style="width: 26%" />
                <col style="width: 26%" />
                <col style="width: 14%" />
              </colgroup>
              <thead>
                <tr>
                  <th>URL</th>
                  <th>Forwards to</th>
                  <th>Router</th>
                  <th>TLS</th>
                </tr>
              </thead>
              <tbody class="stagger">
                <tr v-for="(r, i) in rt.routes" :key="r.router" :style="{ '--i': i }">
                  <td class="cell-wrap" data-label="url">
                    <div v-for="h in r.hosts" :key="h">
                      <a v-if="!h.startsWith('(pattern)')" :href="`https://${h}`" target="_blank" rel="noreferrer">
                        {{ h }}
                      </a>
                      <span v-else class="mono mute">{{ h }}</span>
                    </div>
                    <span v-if="!r.hosts.length" class="mute">no host in the rule</span>
                  </td>
                  <td data-label="forwards to">
                    <RouteTarget :route="r" />
                    <!-- Under swarm this is the SERVICE name; either way it is matched, not read. -->
                    <div class="mute cell-clip" style="font-size: var(--t-sm)" :title="r.container">
                      {{ r.container }}
                    </div>
                  </td>
                  <td data-label="router">
                    <div class="cell-clip" :title="r.router">{{ r.router }}</div>
                    <div v-if="r.priority" class="mute" style="font-size: var(--t-sm)">
                      priority {{ r.priority }}
                    </div>
                  </td>
                  <td data-label="tls">
                    <span v-if="r.tls" class="badge ok">{{ r.certresolver || 'inherited' }}</span>
                    <span v-else class="badge off">off</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!--
            The reported failure, stated where it will be looked for. pstack does not write these
            labels — the deployment's own compose file does — so the fix is in the user's file.
          -->
          <div v-else class="banner warn">
            <b>No routes — Traefik labels come from your compose file.</b>
          </div>
        </section>

        <!-- ============================ containers ============================ -->
        <section class="panel">
          <div class="phead">
            <h2 class="section">Containers</h2>
            <span class="grow" />
            <span class="mute" style="font-size: var(--t-sm)">
              {{ running }} of {{ rt.containers.length }} running
            </span>
            <RefreshButton :run="load" :busy="loading" />
          </div>

          <div v-if="rt.containers.length" class="table-scroll">
            <table class="cards tbl-fixed t-containers" :class="{ swarm: hasNode }">
              <!--
                THE NODE `<col>` CARRIES THE SAME `v-if` AS ITS `<th>`, and has to: one without the
                other shifts every column after it by one, which is the header-drift bug rebuilt by
                hand, in the swarm case that is already the worst one.

                Image takes no width — under fixed layout the unsized column absorbs what is left,
                so adding Node narrows the image rather than pushing the table wider. Actions is in
                px because it holds buttons, not text: five of them at their widest (Stop, Restart,
                Logs, Shell, Open), which `.row-actions` forbids to wrap, so the column is measured
                from the buttons rather than guessed. The table's floor in `app.css` is this
                colgroup added up; change one and change the other.
              -->
              <colgroup>
                <col style="width: 22ch" />
                <col v-if="hasNode" style="width: 14ch" />
                <col style="width: 12ch" />
                <col style="width: 13ch" />
                <col style="width: 20ch" />
                <col />
                <col style="width: 288px" />
              </colgroup>
              <thead>
                <tr>
                  <th>Service</th>
                  <!-- Where, then how it is doing — the order the cells are in. They disagreed, so
                       under swarm (the only time this column exists) every state read as a node. -->
                  <th v-if="hasNode">Node</th>
                  <th>State</th>
                  <th>Ports</th>
                  <th>Networks</th>
                  <th>Image</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody class="stagger">
                <tr v-for="(c, i) in rt.containers" :key="c.id" :style="{ '--i': i }">
                  <td class="name" data-label="service">
                    <div class="cell-clip" :title="c.service ?? c.name">{{ c.service ?? c.name }}</div>
                    <div class="mute task cell-clip" :title="c.name">{{ taskOf(c.name) }}</div>
                  </td>
                  <td v-if="hasNode" data-label="node">
                    <div class="cell-clip" :title="c.node ?? undefined">{{ c.node ?? '—' }}</div>
                    <!-- A task on another node: listed from the manager, out of reach of exec/stop. -->
                    <span v-if="c.remote" class="badge" title="on another node — logs reach it, a shell and stop/start do not">remote</span>
                  </td>
                  <td data-label="state">
                    <span :class="c.state === 'running' ? 's-ok' : 's-failed'">{{ sentence(c.state) }}</span>
                    <!--
                      Health is only reported while it is RUNNING. Docker keeps the last probe result on
                      a stopped container, so an exited one rendered "Exited / Healthy" — a stale reading
                      presented beside the fact that contradicts it.
                    -->
                    <div
                      v-if="c.health && c.state === 'running'"
                      class="mute"
                      style="font-size: var(--t-sm)"
                      :class="c.health === 'healthy' ? 's-ok' : 's-failed'"
                    >
                      {{ sentence(c.health) }}
                    </div>
                  </td>
                  <td data-label="ports">
                    <div v-for="p in c.ports" :key="`${p.containerPort}/${p.protocol}`">
                      <span class="mono">{{ p.containerPort }}</span>
                      <span v-if="p.hostPort" class="mute"> ← host {{ p.hostPort }}</span>
                    </div>
                    <span v-if="!c.ports.length" class="mute">none exposed</span>
                  </td>
                  <td data-label="networks">
                    <div v-for="n in c.networks" :key="n" class="cell-clip" :title="n">
                      <span :class="n === 'preview-ingress' ? 's-ok' : ''">{{ n }}</span>
                    </div>
                    <div v-if="c.ingressIp" class="mute mono cell-clip" style="font-size: var(--t-sm)" :title="c.ingressIp">
                      {{ c.ingressIp }}
                    </div>
                  </td>
                  <!-- Clipped, not wrapped: an image reference is matched against one you know, and
                       `registry.example.com/team/app@sha256:…` costs three rows to say nothing new. -->
                  <td class="dim" data-label="image">
                    <span class="cell-clip" :title="c.image">{{ c.image }}</span>
                  </td>
                  <!--
                    The three things anyone wants from a container row, without hunting for the tab
                    that does it. Open goes to the URL Traefik actually assembled for THIS container
                    (see `urlFor`), so it is absent rather than wrong when no router points here.
                  -->
                  <td class="row-actions" data-label="">
                    <!--
                      One container, not the service and not the stack. Start appears only when it is
                      not running and Stop only when it is, so the control on screen is the one that
                      would do something. Both destructive ones confirm in place.
                    -->
                    <!-- None of these reach a task on another node; docker's verbs are node-local. -->
                    <!-- And none of them reach a one-shot job's row, which stands for the SERVICE:
                         there is no container behind its name, so the verbs could only fail. -->
                    <span v-if="c.job" class="badge" title="a one-shot job — no container to start, stop, or shell into">one-shot</span>
                    <span v-else-if="c.remote" class="mute" style="font-size: var(--t-sm)" title="on another node — redeploy the stack, or act on the worker itself">
                      on {{ c.node }}
                    </span>
                    <ActionButton
                      v-else-if="c.state !== 'running'"
                      class="sm"
                      variant="ghost"
                      :pending="busy === `start:${c.name}`"
                      :disabled="!!busy"
                      confirm="Start it?"
                      @run="act(c, 'start')"
                    >
                      Start
                    </ActionButton>
                    <ActionButton
                      v-else
                      class="sm"
                      variant="ghost"
                      :pending="busy === `stop:${c.name}`"
                      :disabled="!!busy"
                      confirm="Stop it?"
                      title="This container only — it stays stopped until something starts it."
                      @run="act(c, 'stop')"
                    >
                      Stop
                    </ActionButton>
                    <ActionButton
                      v-if="!c.remote && !c.job"
                      class="sm"
                      variant="ghost"
                      :pending="busy === `restart:${c.name}`"
                      :disabled="!!busy"
                      confirm="Restart it?"
                      @run="act(c, 'restart')"
                    >
                      Restart
                    </ActionButton>
                    <RouterLink
                      class="btn sm ghost"
                      :to="`/deployments/${encodeURIComponent(dep.id)}/logs?container=${encodeURIComponent(c.name)}`"
                    >
                      Logs
                    </RouterLink>
                    <RouterLink
                      v-if="c.state === 'running' && !c.remote && !c.job"
                      class="btn sm ghost"
                      :to="`/deployments/${encodeURIComponent(dep.id)}/terminal?container=${encodeURIComponent(c.name)}`"
                    >
                      Shell
                    </RouterLink>
                    <!--
                      Only while running: the router label survives a stopped container, so the URL
                      still exists and answers 502. A link that is certain to fail is worse than no link.
                    -->
                    <a
                      v-if="c.state === 'running' && urlFor(c)"
                      class="btn sm ghost"
                      :href="urlFor(c)!"
                      target="_blank"
                      rel="noreferrer"
                    >
                      Open
                    </a>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="mute">No containers for this stack.</p>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
/*
 * The second line of the service cell: the task, under the service it belongs to.
 *
 * The `max-width` cap is GONE. It was a ceiling on what this line could bid for under auto layout —
 * a bid is what auto layout runs on, and there is no bidding any more: the `<colgroup>` sets the
 * column and `.cell-clip` on the line keeps it inside. Two mechanisms for one width is how they
 * drift apart. `taskOf` still shortens what is shown; `title` still holds the whole name.
 */
.task {
  font-size: var(--t-sm);
}

/*
 * The floors that make `.table-scroll` a scroller rather than decoration live in `app.css`, beside
 * the `.tbl-fixed` comment that promises them — one place, so a floor and the rule it belongs to
 * cannot be changed apart. They are derived from the `<colgroup>`s above: change a `<col>` and go
 * change the matching `min-width` there.
 */
</style>
