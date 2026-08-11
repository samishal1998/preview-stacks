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
 * half is visible rather than inferred.
 *
 * Findings come first because they are the reason anyone opened this tab.
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
import InfoHint from '../../components/InfoHint.vue';
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

const errors = computed(() => rt.value?.findings.filter((f) => f.level === 'error') ?? []);
const warns = computed(() => rt.value?.findings.filter((f) => f.level === 'warn') ?? []);
const infos = computed(() => rt.value?.findings.filter((f) => f.level === 'info') ?? []);
const running = computed(() => rt.value?.containers.filter((c) => c.state === 'running').length ?? 0);

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
    .filter((r) => r.container === c.name)
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
        <b>Docker did not answer.</b>
        <p>
          Nothing below could be determined — this is not the same as nothing running. Check the
          daemon and the socket mount, then refresh.
        </p>
      </div>

      <template v-else>
        <!-- Findings first: this tab exists because something is not working. -->
        <div v-for="(f, i) in errors" :key="`e${i}`" class="banner failed">
          <b>Blocking</b>
          <p>{{ f.message }}</p>
        </div>
        <div v-for="(f, i) in warns" :key="`w${i}`" class="banner warn">
          <b>Worth checking</b>
          <p>{{ f.message }}</p>
        </div>
        <div v-for="(f, i) in infos" :key="`i${i}`" class="banner plain">
          <p>{{ f.message }}</p>
        </div>
        <div v-if="!rt.findings.length && rt.containers.length" class="banner ok">
          <b>Nothing looks wrong.</b>
          <p>
            Every container is attached to the ingress network and every router has a rule and a port.
            If a hostname still fails, the next suspects are DNS and the certificate.
          </p>
        </div>

        <!-- ============================ routing ============================ -->
        <section class="panel">
          <div class="phead">
            <h2 class="section">Routing</h2>
            <span class="grow" />
            <span class="mute" style="font-size: var(--t-sm)">
              TLS: {{ rt.challenge === 'unknown' ? 'unknown' : rt.challenge }}
              <InfoHint label="why the TLS mode matters here" align="end">
                Under HTTP-01 every per-PR router needs <code>tls.certresolver=le</code>, because each
                hostname resolves its own certificate. Under DNS-01 that inverts — one always-on router
                holds the wildcard, and a per-PR certresolver makes each PR order its own and burn the
                weekly limit. Read from the running Traefik's own flags, not configured here.
              </InfoHint>
            </span>
          </div>

          <table v-if="rt.routes.length" class="cards">
            <thead>
              <tr>
                <th>URL</th>
                <th>
                  Forwards to
                  <InfoHint label="how a URL reaches a port">
                    Traefik's docker provider resolves the container's IP on
                    <code>preview-ingress</code> and dials it on the port from
                    <code>loadbalancer.server.port</code> — it does <em>not</em> use
                    <code>service:port</code> DNS. So the port here is the one inside the container,
                    and publishing a host port is unnecessary.
                  </InfoHint>
                </th>
                <th>Router</th>
                <th>TLS</th>
              </tr>
            </thead>
            <tbody class="stagger">
              <tr v-for="(r, i) in rt.routes" :key="r.router" :style="{ '--i': i }">
                <td data-label="url">
                  <div v-for="h in r.hosts" :key="h">
                    <a v-if="!h.startsWith('(pattern)')" :href="`https://${h}`" target="_blank" rel="noreferrer">
                      {{ h }}
                    </a>
                    <span v-else class="mono mute">{{ h }}</span>
                  </div>
                  <span v-if="!r.hosts.length" class="mute">no host in the rule</span>
                </td>
                <td data-label="forwards to">
                  <span v-if="r.target" class="mono">{{ r.target }}</span>
                  <span v-else class="s-failed">
                    {{ r.port === null ? 'no port declared' : 'not on the ingress network' }}
                  </span>
                  <div class="mute" style="font-size: var(--t-sm)">{{ r.container }}</div>
                </td>
                <td data-label="router">
                  {{ r.router }}
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

          <!--
            The reported failure, stated where it will be looked for. pstack does not write these
            labels — the deployment's own compose file does — so the fix is in the user's file.
          -->
          <div v-else class="banner warn">
            <b>No routes. Traefik has not been told about this deployment.</b>
            <p>
              Routes come from labels in <em>your</em> compose file, not from pstack and not from the
              <RouterLink to="/routing">Routing</RouterLink> page (that is Traefik's file provider —
              middleware and TLS options, not per-PR routers). A reachable service needs four things:
            </p>
            <pre class="code">labels:
  - traefik.enable=true                     # the host runs exposedbydefault=false
  - traefik.docker.network=preview-ingress  # which network Traefik should dial
  - traefik.http.routers.&lt;name&gt;-${STACK}.rule=Host(`&lt;name&gt;-${STACK}.${PREVIEW_DOMAIN}`)
  - traefik.http.services.&lt;name&gt;-${STACK}.loadbalancer.server.port=80
networks: [default, preview-ingress]        # and preview-ingress: { external: true }</pre>
            <p class="mute">
              Put <code>${STACK}</code> in the router and service names: Traefik's namespace is global
              across the host, so two deployments sharing a router name serve each other's containers.
            </p>
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

          <table v-if="rt.containers.length" class="cards">
            <thead>
              <tr>
                <th>Service</th>
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
                  {{ c.service ?? c.name }}
                  <div class="mute" style="font-size: var(--t-sm)">{{ c.name }}</div>
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
                  <div v-for="n in c.networks" :key="n">
                    <span :class="n === 'preview-ingress' ? 's-ok' : ''">{{ n }}</span>
                  </div>
                  <div v-if="c.ingressIp" class="mute mono" style="font-size: var(--t-sm)">
                    {{ c.ingressIp }}
                  </div>
                </td>
                <td class="dim" data-label="image">{{ c.image }}</td>
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
                  <ActionButton
                    v-if="c.state !== 'running'"
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
                    title="Stops this container only. It stays stopped until something starts it."
                    @run="act(c, 'stop')"
                  >
                    Stop
                  </ActionButton>
                  <ActionButton
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
                    v-if="c.state === 'running'"
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
          <p v-else class="mute">
            No containers for this stack. If you expected some, check that their profile is one the
            spec selects — a service behind a profile that is not enabled is never started.
          </p>
        </section>
      </template>
    </template>
  </div>
</template>
