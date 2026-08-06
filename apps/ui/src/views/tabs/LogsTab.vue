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
import SelectMenu from '../../components/SelectMenu.vue';
import LogViewer from '../../components/LogViewer.vue';
import type { LogRow } from '../../components/log-viewer-types';

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

/** Empty value = the whole stack, which is a real choice here rather than a placeholder. */
const serviceOptions = computed(() => [
  { value: '', label: 'All services' },
  ...services.value.map((s) => ({ value: s, label: s })),
]);

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

/**
 * Raw is ALWAYS available, one click away. The pretty view re-arranges compose's output, and a
 * re-arranged log is the wrong thing to paste into a bug report or diff against a colleague's —
 * raw shows the exact bytes the server sent, filter and all.
 */
const raw = ref(false);

/**
 * Compose prefixes every line with `container-name  | `. The pretty view lifts that prefix into a
 * non-wrapping gutter with a stable per-service colour, so interleaved services stop reading as one
 * grey wall. A line with no prefix (a compose warning, a continuation) gets an empty gutter rather
 * than being guessed at.
 */
const PREFIX = /^(\S+)\s+\| /;
const logRows = computed<LogRow[]>(() => {
  const text = shown.value;
  if (!text || text.startsWith('(no line matches')) return [];
  const hues = new Map<string, number>();
  return text.split('\n').map((line, i) => {
    const m = PREFIX.exec(line);
    if (!m) return { key: i, text: line };
    const svc = m[1]!;
    if (!hues.has(svc)) hues.set(svc, hues.size % 6);
    return {
      key: i,
      gutter: svc,
      text: line.slice(m[0].length),
      tone: `svc-${hues.get(svc)}`,
    };
  });
});
</script>

<template>
  <section class="panel">
    <div class="phead">
      <h2 class="section">Compose logs</h2>
      <span class="grow" />
      <div v-if="services.length > 1" class="field">
        <label for="svc" class="mute" style="font-size: var(--t-xs)">service</label>
        <SelectMenu v-model="service" id="svc" label="Service" :options="serviceOptions" />
      </div>
      <div class="field">
        <label for="tail" class="mute" style="font-size: var(--t-xs)">tail</label>
        <SelectMenu :model-value="String(tail)" label="How many lines" :options="[{ value: '50', label: '50' }, { value: '200', label: '200' }, { value: '500', label: '500' }, { value: '1000', label: '1000' }, { value: '2000', label: '2000' }]" @update:model-value="(v) => (tail = Number(v))" />
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
        host. The server caps how many lines it will return.
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

      <template v-if="logs">
        <pre v-if="raw" class="log">{{ shown || '(no output)' }}</pre>
        <LogViewer v-else :rows="logRows" :copy-text="shown" empty-text="(no output)" />
        <div class="row" style="margin-top: var(--s2)">
          <label class="check">
            <input v-model="raw" type="checkbox" />
            Raw output
            <InfoHint label="when raw matters">
              The pretty view lifts each line's service prefix into a coloured column. Raw is the
              exact text the server sent — the form to paste into a bug report or compare with
              someone else's copy.
            </InfoHint>
          </label>
        </div>
      </template>
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
