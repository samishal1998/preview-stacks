<script setup lang="ts">
/**
 * Compose logs, fetched ON DEMAND.
 *
 * Never polled: an auto-refresh against a chatty stack would hammer the host through
 * `docker compose logs` — the same host that is running everyone else's previews. The tail is
 * clamped to 1–2000 by the server for the same reason.
 *
 * ── THE CONTAINER LIST IS A PANEL, NOT A DROPDOWN ────────────────────────────────────────────────
 *
 * A picker hides the fleet behind a click and shows one name at a time. Reading logs is a hunt —
 * "which of these is the one that's broken" — so the containers are a standing list beside the
 * output, each with its state, and one click switches the pane. It scrolls, because a stack with
 * twenty containers must not push the log off the screen.
 *
 * Selecting a CONTAINER narrows two ways at once: the server is asked for that container's service
 * (so the whole tail is spent on it), and rows whose prefix names a different replica are dropped
 * client-side. Selecting "All services" restores the interleaved view.
 *
 * ── WHY THE OPTIONS EXIST ────────────────────────────────────────────────────────────────────────
 *
 * `--timestamps` is what makes a line comparable to anything else — a deploy, a healthcheck flap,
 * another service. `--since` is what makes a long-lived container readable: a tail of 2000 on a
 * service that logs every request is 2000 lines of the last four minutes.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { api, problem, query } from '../../api/client';
import type { LogsResponse, RuntimeContainer, RuntimeResponse } from '../../api/types';
import { dep } from '../../composables/useDeployment';
import { sentence } from '../../composables/useFormat';
import SelectMenu from '../../components/SelectMenu.vue';
import LogViewer from '../../components/LogViewer.vue';
import type { LogRow } from '../../components/log-viewer-types';

const route = useRoute();
const router = useRouter();

const tail = ref(200);
const since = ref('');
const timestamps = ref(false);
/** '' means the whole stack; otherwise a CONTAINER name (its service is what the server is asked for). */
const selected = ref('');
const containers = ref<RuntimeContainer[]>([]);

const current = computed(() => containers.value.find((c) => c.name === selected.value) ?? null);
/** What the server is asked to narrow to — compose narrows by service, not by container. */
const service = computed(() => current.value?.service ?? '');

async function loadContainers(): Promise<void> {
  const r = await api.get<RuntimeResponse>(
    `/api/deployments/${encodeURIComponent(dep.id)}/runtime${query(dep.vars)}`,
  );
  if (!r.ok) return; // the panel is a convenience; the whole-stack read still works without it
  containers.value = [...r.body.containers].sort((a, b) =>
    (a.service ?? a.name).localeCompare(b.service ?? b.name),
  );
}

const filter = ref('');
const logs = ref<LogsResponse | null>(null);
const error = ref('');
const loading = ref(false);

watch(
  () => [dep.id, !!dep.detail?.compose] as const,
  ([id, hasCompose], prev) => {
    if (!prev || prev[0] !== id) {
      containers.value = [];
      selected.value = '';
      logs.value = null;
    }
    // Gated on the RESOLVED spec, not just the id: this component mounts before the parent has
    // resolved the deployment, so an id-only watcher saw `dep.detail === undefined` and the
    // container panel never appeared.
    if (hasCompose && containers.value.length === 0) void loadContainers();
  },
  { immediate: true },
);

/**
 * `?container=` deep link — how the Containers table jumps straight here.
 *
 * Applied once the container list has arrived, because the name has to resolve to a real container
 * to pick its service; and it fetches immediately, since arriving from "show me this container's
 * logs" and then being asked to press Fetch is a wasted click.
 */
watch(
  [containers, () => route.query.container],
  ([list, wanted]) => {
    const name = typeof wanted === 'string' ? wanted : '';
    if (!name || !list.length || selected.value === name) return;
    if (!list.some((c) => c.name === name)) return;
    selected.value = name;
    void fetchLogs();
  },
  { immediate: true },
);

function select(name: string): void {
  if (selected.value === name) return;
  selected.value = name;
  // Keep the URL honest so the pane can be linked to and survives a reload.
  void router.replace({ query: { ...route.query, container: name || undefined } });
  void fetchLogs();
}

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
      ...(since.value ? { since: since.value } : {}),
      ...(timestamps.value ? { timestamps: 1 } : {}),
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

/**
 * FOLLOW — one `compose logs --follow` on the server, streamed here over SSE.
 *
 * Not a poll. Re-fetching a full tail every few seconds would re-send everything each time and hit
 * the host that is running everyone else's previews; a followed process sends each line once and
 * costs nothing in between. That is why the "never polled" rule in this file survives a live tail.
 *
 * The buffer is CAPPED. A chatty service can emit thousands of lines a minute, and an unbounded
 * array behind a DOM list is how a tab left open overnight ends up unresponsive.
 */
const MAX_FOLLOW_LINES = 5_000;
const following = ref(false);
const liveLines = ref<string[]>([]);
let source: EventSource | null = null;

function stopFollow(): void {
  source?.close();
  source = null;
  following.value = false;
}

function startFollow(): void {
  stopFollow();
  liveLines.value = [];
  const q = query(dep.vars, {
    tail: tail.value,
    ...(service.value ? { service: service.value } : {}),
    ...(timestamps.value ? { timestamps: 1 } : {}),
  });
  // EventSource cannot set headers, which is exactly why sessions are cookies — the request is
  // same-origin and carries the session automatically. A token-only browser session is the one case
  // this cannot serve, and the server answers 401 rather than streaming nothing.
  source = new EventSource(
    api.url(`/api/deployments/${encodeURIComponent(dep.id)}/logs/stream${q}`),
  );
  following.value = true;
  source.onmessage = (ev) => {
    const payload = JSON.parse(ev.data) as { line?: string; done?: boolean; reason?: string };
    if (payload.done) {
      // The stack stopped, or the follow hit its cap. Say which, rather than leaving a live-looking
      // pane that will never produce another line.
      liveLines.value.push(`— ${payload.reason ?? 'stream ended'} —`);
      stopFollow();
      return;
    }
    if (payload.line === undefined) return;
    liveLines.value.push(payload.line);
    if (liveLines.value.length > MAX_FOLLOW_LINES) {
      liveLines.value.splice(0, liveLines.value.length - MAX_FOLLOW_LINES);
    }
  };
  source.onerror = () => stopFollow();
}

function toggleFollow(): void {
  if (following.value) stopFollow();
  else startFollow();
}

// Switching container/options mid-follow must re-open the stream, or the pane keeps showing the
// previous selection's lines under the new label.
watch([selected, timestamps, tail], () => {
  if (following.value) startFollow();
});
onBeforeUnmount(stopFollow);

/** Search-in-logs: filter to matching lines rather than highlighting inside a wall of text. */
const shown = computed(() => {
  // While following, the live buffer IS the log — one source of truth for the pane, so the pretty
  // view, raw view, search and copy all agree about what is on screen.
  const text = following.value || liveLines.value.length ? liveLines.value.join('\n') : (logs.value?.text ?? '');
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
 * Compose prefixes every line with `container-name  | `, and with `--timestamps` the timestamp
 * follows it. The pretty view lifts the prefix into a non-wrapping gutter with a stable per-service
 * colour and the timestamp into the viewer's own time column, so interleaved services stop reading
 * as one grey wall. A line with no prefix (a compose warning, a continuation) gets an empty gutter
 * rather than being guessed at.
 */
const PREFIX = /^(\S+)\s+\| /;
/** Docker's own stamp: RFC3339 with nanoseconds, always first after the prefix. */
const TS = /^(\d{4}-\d{2}-\d{2}T[\d:.]+Z?)\s/;

const logRows = computed<LogRow[]>(() => {
  const text = shown.value;
  if (!text || text.startsWith('(no line matches')) return [];
  const hues = new Map<string, number>();
  const only = current.value?.name ?? '';
  const rows: LogRow[] = [];
  text.split('\n').forEach((line, i) => {
    const m = PREFIX.exec(line);
    if (!m) {
      rows.push({ key: i, text: line });
      return;
    }
    const from = m[1]!;
    // A service with several replicas answers as one stream; keep only the chosen replica's lines.
    if (only && from !== only && containers.value.some((c) => c.name === from)) return;
    let rest = line.slice(m[0].length);
    let when: string | undefined;
    const t = TS.exec(rest);
    if (t) {
      when = t[1]!;
      rest = rest.slice(t[0].length);
    }
    if (!hues.has(from)) hues.set(from, hues.size % 6);
    rows.push({
      key: i,
      gutter: from,
      text: rest,
      tone: `svc-${hues.get(from)}`,
      ...(when ? { time: new Date(when).toTimeString().slice(0, 8), timeTitle: when } : {}),
    });
  });
  return rows;
});

const stateTone = (c: RuntimeContainer): string =>
  c.state === 'running' && c.health !== 'unhealthy' ? 's-ok' : c.state === 'exited' && c.exitCode === 0 ? 'mute' : 's-failed';
</script>

<template>
  <section class="panel">
    <div class="phead">
      <h2 class="section">Compose logs</h2>
      <span class="grow" />
      <div class="field inline">
        <label for="tail">Lines</label>
        <SelectMenu
          id="tail"
          :model-value="String(tail)"
          label="How many lines"
          :options="[
            { value: '50', label: '50' },
            { value: '200', label: '200' },
            { value: '500', label: '500' },
            { value: '1000', label: '1000' },
            { value: '2000', label: '2000' },
          ]"
          @update:model-value="(v) => (tail = Number(v))"
        />
      </div>
      <div class="field inline">
        <label for="since">Since</label>
        <SelectMenu
          id="since"
          v-model="since"
          label="How far back"
          :options="[
            { value: '', label: 'Anything kept' },
            { value: '5m', label: 'Last 5 minutes' },
            { value: '30m', label: 'Last 30 minutes' },
            { value: '1h', label: 'Last hour' },
            { value: '24h', label: 'Last day' },
          ]"
        />
      </div>
      <label class="check">
        <input v-model="timestamps" type="checkbox" />
        Timestamps
      </label>
      <button
        :class="following ? 'danger' : ''"
        :disabled="!dep.detail?.compose"
        :title="following ? 'Stop streaming' : 'Stream new lines as they arrive'"
        @click="toggleFollow"
      >
        {{ following ? 'Stop following' : 'Follow' }}
      </button>
      <button
        class="primary"
        :disabled="loading || following || !dep.detail?.compose"
        :title="following ? 'Already streaming.' : undefined"
        @click="fetchLogs"
      >
        {{ loading ? 'Reading…' : logs ? 'Refresh' : 'Fetch' }}
        <span v-if="loading" class="progress" aria-hidden="true" />
      </button>
    </div>

    <p v-if="!dep.detail" class="mute">Unavailable until the spec resolves.</p>
    <!-- Guard on the field rather than calling and interpreting the failure after the fact. -->
    <p v-else-if="!dep.detail.compose" class="mute">No <code>compose:</code> section.</p>

    <template v-else>
      <div v-if="error" class="banner failed">{{ error }}</div>

      <!--
        `ok: false` means compose exited non-zero. Usually "no such project" because the stack was
        never brought up — not a server fault, so it is a note rather than an error.
      -->
      <div v-else-if="logs && !logs.ok" class="banner warn">
        <b>{{ dep.detail?.orchestrator === 'swarm' ? 'docker service logs' : 'Compose' }} exited non-zero.</b>
        <p>
          <!-- Asleep and never-brought-up look identical from here; say which. -->
          <template v-if="dep.detail?.asleep">Asleep — no containers to read.</template>
          <template v-else>Usually nothing brought up yet; any output it produced is below.</template>
        </p>
      </div>

      <div class="logs-split">
        <!-- ============================ the container panel ============================ -->
        <aside class="clist" aria-label="Containers">
          <button class="clist-item" :class="{ on: selected === '' }" @click="select('')">
            <span class="clist-name">All services</span>
            <span class="clist-sub">{{ containers.length }} container{{ containers.length === 1 ? '' : 's' }}</span>
          </button>
          <button
            v-for="c in containers"
            :key="c.id"
            class="clist-item"
            :class="{ on: selected === c.name }"
            @click="select(c.name)"
          >
            <span class="clist-name">
              {{ c.service ?? c.name }}
              <span v-if="c.restartCount > 0" class="badge warn">{{ c.restartCount }}×</span>
            </span>
            <span class="clist-sub mono">{{ c.name }}</span>
            <span class="clist-state" :class="stateTone(c)">
              {{ sentence(c.health ?? c.state) }}
            </span>
          </button>
          <p v-if="!containers.length" class="hint">No containers.</p>
        </aside>

        <!-- ============================ the output ============================ -->
        <div class="logs-main">
          <div class="row" style="margin-bottom: var(--s3)">
            <div class="field grow">
              <label for="lf">Search</label>
              <input
                id="lf"
                v-model="filter"
                data-search
                type="search"
                placeholder="Substring"
                spellcheck="false"
              />
            </div>
            <label class="check">
              <input v-model="raw" type="checkbox" />
              Raw output
            </label>
          </div>

          <template v-if="logs || following || liveLines.length">
            <pre v-if="raw" class="log">{{ shown || '(no output)' }}</pre>
            <LogViewer
              v-else
              :rows="logRows"
              :live="following"
              :copy-text="shown"
              :empty-text="following ? 'Waiting for output…' : '(no output)'"
            />
            <p v-if="following" class="hint">
              Following {{ current ? current.name : 'the whole stack' }} · last
              {{ MAX_FOLLOW_LINES.toLocaleString() }} lines
            </p>
            <!-- The redaction note stays: what is on screen is not the raw bytes on the host. -->
            <p v-else-if="logs" class="hint">
              {{ logs.lines ?? 0 }} line{{ (logs.lines ?? 0) === 1 ? '' : 's' }}
              <template v-if="current">from <b>{{ current.name }}</b></template>
              <template v-else>across the stack</template>
              <template v-if="logs.since">, last {{ logs.since }}</template>, tail
              {{ logs.tail }} · secrets redacted on the host.
            </p>
          </template>
          <p v-else-if="!loading" class="mute">Fetch the last lines, or follow live.</p>
        </div>
      </div>
    </template>
  </section>
</template>
