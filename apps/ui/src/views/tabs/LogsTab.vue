<script setup lang="ts">
/**
 * Compose logs, fetched ON DEMAND.
 *
 * Never polled: an auto-refresh against a chatty stack would hammer the host through
 * `docker compose logs` — the same host that is running everyone else's previews. The tail is
 * clamped to 1–2000 by the server for the same reason.
 */
import { computed, ref, watch } from 'vue';
import { api, problem, query } from '../../api/client';
import type { LogsResponse, RuntimeResponse } from '../../api/types';
import { dep } from '../../composables/useDeployment';
import InfoHint from '../../components/InfoHint.vue';

const tail = ref(200);
/** '' means the whole stack. */
const service = ref('');
/**
 * The services to choose from, taken from what is actually RUNNING rather than from the compose file.
 * A service the spec declares but never started has no logs, and offering it would produce an
 * empty pane that reads like a bug.
 */
const services = ref<string[]>([]);

async function loadServices(): Promise<void> {
  const r = await api.get<RuntimeResponse>(
    `/api/deployments/${encodeURIComponent(dep.id)}/runtime${query(dep.vars)}`,
  );
  if (!r.ok) return; // the picker is a convenience; without it the whole-stack read still works
  services.value = [
    ...new Set(r.body.containers.map((c) => c.service).filter((s): s is string => !!s)),
  ].sort();
}

const filter = ref('');
const logs = ref<LogsResponse | null>(null);
const error = ref('');
const loading = ref(false);

watch(
  () => [dep.id, !!dep.detail?.compose] as const,
  ([id, hasCompose], prev) => {
    if (!prev || prev[0] !== id) {
      services.value = [];
      service.value = '';
      logs.value = null;
    }
    // Gated on the RESOLVED spec, not just the id: this component mounts before the parent has
    // resolved the deployment, so an id-only watcher saw `dep.detail === undefined` and the service
    // picker never appeared.
    if (hasCompose && services.value.length === 0) void loadServices();
  },
  { immediate: true },
);

async function fetchLogs(): Promise<void> {
  if (!dep.detail?.compose) return;
  const id = dep.id;
  loading.value = true;
  error.value = '';
  const chosen = service.value;
  const r = await api.get<LogsResponse>(
    `/api/deployments/${encodeURIComponent(id)}/logs${query(dep.vars, {
      tail: tail.value,
      ...(chosen ? { service: chosen } : {}),
    })}`,
  );
  loading.value = false;
  if (dep.id !== id) return; // navigated away mid-flight
  // A slow response for the previously-selected service must not land in a pane now labelled with a
  // different one.
  if (r.ok && (r.body.service ?? '') !== chosen) return;
  if (!r.ok) {
    logs.value = null;
    error.value = problem(r, 'load the logs');
    return;
  }
  logs.value = { ...r.body, ok: r.body.ok !== false, text: r.body.text ?? '' };
}

/** Search-in-logs: filter to matching lines rather than highlighting inside a wall of text. */
const shown = computed(() => {
  const text = logs.value?.text ?? '';
  const needle = filter.value.trim().toLowerCase();
  if (!needle) return text;
  const lines = text.split('\n').filter((l) => l.toLowerCase().includes(needle));
  return lines.length ? lines.join('\n') : `(no line matches "${filter.value}")`;
});
</script>

<template>
  <section class="panel">
    <div class="phead">
      <h2 class="section">Compose logs</h2>
      <span class="grow" />
      <div v-if="services.length > 1" class="field">
        <label for="svc" class="mute" style="font-size: var(--t-xs)">service</label>
        <select id="svc" v-model="service">
          <option value="">all services</option>
          <option v-for="s in services" :key="s" :value="s">{{ s }}</option>
        </select>
      </div>
      <div class="field">
        <label for="tail" class="mute" style="font-size: var(--t-xs)">tail</label>
        <select id="tail" v-model.number="tail">
          <option :value="50">50</option>
          <option :value="200">200</option>
          <option :value="500">500</option>
          <option :value="1000">1000</option>
          <option :value="2000">2000</option>
        </select>
      </div>
      <button
        class="primary"
        style="align-self: end"
        :disabled="loading || !dep.detail?.compose"
        @click="fetchLogs"
      >
        {{ loading ? 'reading…' : 'Fetch' }}
        <span v-if="loading" class="progress" aria-hidden="true" />
      </button>
    </div>

    <p v-if="!dep.detail" class="mute">Unavailable until the spec resolves.</p>
    <!-- Guard on the field rather than calling and interpreting the failure after the fact. -->
    <p v-else-if="!dep.detail.compose" class="mute">
      This spec has no <code>compose:</code> section, so there are no compose logs to read. Its axes
      may still have created resources — check those at their own source.
    </p>

    <template v-else>
      <p class="hint" style="margin: 0 0 var(--s3)">
        Fetched on demand, never polled: an auto-refresh against a chatty stack would hammer the
        host. <code>tail</code> is clamped to 1–2000 by the server.
      </p>

      <div v-if="error" class="banner failed">{{ error }}</div>

      <!--
        `ok: false` means compose exited non-zero. Usually "no such project" because the stack was
        never brought up — not a server fault, so it is a note rather than an error.
      -->
      <div v-else-if="logs && !logs.ok" class="banner warn">
        <b>compose exited non-zero.</b>
        <p>
          Most often the project does not exist yet (nothing has been brought up), or a profile is
          missing. Any output it did produce is below.
        </p>
      </div>

      <div v-if="logs" class="field" style="margin-top: var(--s3)">
        <label for="lf" class="mute" style="font-size: var(--t-xs)">Search in these lines</label>
        <input id="lf" v-model="filter" data-search type="search" placeholder="substring" spellcheck="false" />
      </div>

      <div v-if="logs && logs.service" class="hint" style="margin-top: var(--s3)">
        Showing <b>{{ logs.service }}</b> only.
        <InfoHint label="why narrowing helps">
          On a stack with a chatty sidecar the interleaved output is unreadable, and the lines you want
          are often already past the tail. Narrowing spends the whole tail on one service.
        </InfoHint>
      </div>

      <pre v-if="logs" class="log">{{ shown || '(no output)' }}</pre>
      <p v-else-if="!loading" class="mute" style="margin-top: var(--s3)">
        Press <b>Fetch</b> to read the last {{ tail }} lines{{ service ? ` of ${service}` : '' }}.
      </p>

      <p v-if="logs" class="hint">
        Redacted on the host before sending: credentials inside URLs, <code>NAME=value</code> pairs
        whose name reads as a secret, and this server's own token.
      </p>
    </template>
  </section>
</template>
