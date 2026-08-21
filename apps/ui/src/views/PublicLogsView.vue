<script setup lang="ts">
/**
 * The page a share link opens: `/deployments/:id/public-logs-view?token=…`
 *
 * A visitor here has no account and no session. The link's token — a signed, expiring grant to
 * ONE deployment (share.ts on the server) — rides on every request this page makes, including the
 * EventSource that follows logs, which is why it travels in the query string at all. The api
 * client attaches it while this view is mounted (`setPublicToken`) and forgets it on unmount; it is
 * never stored, and it is never sent anywhere but this origin.
 *
 * What is shown is exactly what the token's views allow: details (the deployment row + its
 * containers) and/or logs. Nothing here acts — there is no button that could, and the server would
 * answer 403 if there were.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { api, setPublicToken } from '../api/client';
import type { Deployment, LogsResponse, RuntimeResponse, ShareView } from '../api/types';
import { sentence, stamp } from '../composables/useFormat';
import LogViewer from '../components/LogViewer.vue';
import SelectMenu from '../components/SelectMenu.vue';
import type { LogRow } from '../components/log-viewer-types';

const props = defineProps<{ id: string }>();
const route = useRoute();

const token = computed(() => String(route.query.token ?? ''));
const views = ref<ShareView[]>([]);
const expiresAt = ref<number | null>(null);
const fatal = ref('');
const detail = ref<Deployment | null>(null);
const runtime = ref<RuntimeResponse | null>(null);

// ── logs ────────────────────────────────────────────────────────────────────────────────────────
const service = ref('');
const timestamps = ref(false);
const following = ref(true);
const lines = ref<string[]>([]);
const logNote = ref('');
let source: EventSource | null = null;
const MAX_LINES = 5_000;

const services = computed(() => {
  const seen = new Set<string>();
  for (const c of runtime.value?.containers ?? []) if (c.service) seen.add(c.service);
  return [...seen];
});

function explain(status: number, what: string): string {
  if (status === 401) return 'This link has expired or is not valid.';
  if (status === 403) return `This link does not allow ${what}.`;
  if (status === 0) return 'The control plane is unreachable.';
  return `Could not load ${what} (HTTP ${status}).`;
}

function stopFollow(): void {
  source?.close();
  source = null;
}

function startFollow(): void {
  stopFollow();
  lines.value = [];
  logNote.value = '';
  const p = new URLSearchParams({ tail: '500' });
  if (service.value) p.set('service', service.value);
  if (timestamps.value) p.set('timestamps', '1');
  source = new EventSource(api.url(`/api/deployments/${encodeURIComponent(props.id)}/logs/stream?${p}`));
  source.onmessage = (ev) => {
    const payload = JSON.parse(ev.data) as { line?: string; done?: boolean; reason?: string };
    if (payload.done) {
      logNote.value = payload.reason ?? 'stream ended';
      stopFollow();
      return;
    }
    if (payload.line === undefined) return;
    lines.value.push(payload.line);
    if (lines.value.length > MAX_LINES) lines.value.splice(0, lines.value.length - MAX_LINES);
  };
  source.onerror = () => {
    logNote.value = 'the stream disconnected — reload to reconnect, or the link has expired';
    stopFollow();
  };
}

async function fetchOnce(): Promise<void> {
  stopFollow();
  const p = new URLSearchParams({ tail: '500' });
  if (service.value) p.set('service', service.value);
  if (timestamps.value) p.set('timestamps', '1');
  const r = await api.get<LogsResponse>(`/api/deployments/${encodeURIComponent(props.id)}/logs?${p}`);
  if (!r.ok) {
    logNote.value = explain(r.status, 'logs');
    return;
  }
  lines.value = (r.body.text ?? '').split('\n').filter(Boolean);
  logNote.value = r.body.ok ? '' : 'the log command exited non-zero — the stack may be asleep or not deployed';
}

function refreshLogs(): void {
  if (following.value) startFollow();
  else void fetchOnce();
}

const PREFIX = /^(\S+)\s+\| /;
const TS = /^(\d{4}-\d{2}-\d{2}T[\d:.]+Z?)\s/;
const rows = computed<LogRow[]>(() => {
  const hues = new Map<string, number>();
  return lines.value.map((line, i) => {
    const m = PREFIX.exec(line);
    if (!m) return { key: i, text: line };
    const from = m[1]!;
    let rest = line.slice(m[0].length);
    let when: string | undefined;
    const t = TS.exec(rest);
    if (t) {
      when = t[1]!;
      rest = rest.slice(t[0].length);
    }
    if (!hues.has(from)) hues.set(from, hues.size % 6);
    return {
      key: i,
      gutter: from,
      text: rest,
      tone: `svc-${hues.get(from)}`,
      ...(when ? { time: new Date(when).toTimeString().slice(0, 8), timeTitle: when } : {}),
    };
  });
});

// ── load ────────────────────────────────────────────────────────────────────────────────────────
async function load(): Promise<void> {
  if (!token.value) {
    fatal.value = 'This link is incomplete — it carries no token.';
    return;
  }
  const me = await api.get<{ share?: { deployment: string; views: ShareView[]; expiresAt: number | null } }>('/api/auth/me');
  if (!me.ok) {
    fatal.value = explain(me.status, 'this page');
    return;
  }
  if (!me.body.share) {
    fatal.value = 'This is not a share link.';
    return;
  }
  if (me.body.share.deployment !== props.id) {
    fatal.value = 'This link belongs to a different deployment.';
    return;
  }
  views.value = me.body.share.views;
  expiresAt.value = me.body.share.expiresAt;

  if (views.value.includes('details')) {
    const [d, rt] = await Promise.all([
      api.get<Deployment>(`/api/deployments/${encodeURIComponent(props.id)}`),
      api.get<RuntimeResponse>(`/api/deployments/${encodeURIComponent(props.id)}/runtime`),
    ]);
    if (d.ok) detail.value = d.body;
    if (rt.ok) runtime.value = rt.body;
  }
  if (views.value.includes('logs')) refreshLogs();
}

const state = computed(() => {
  if (detail.value?.asleep) return { text: 'asleep', cls: 'asleep' };
  if (runtime.value && runtime.value.reachable === false) return { text: 'unknown', cls: 'unknown' };
  if (runtime.value?.containers.some((c) => c.state === 'running')) return { text: 'running', cls: 'ok' };
  if (runtime.value) return { text: 'not running', cls: '' };
  return null;
});

watch([service, timestamps], refreshLogs);
watch(following, refreshLogs);

onMounted(() => {
  setPublicToken(token.value);
  void load();
});
onBeforeUnmount(() => {
  stopFollow();
  setPublicToken('');
});
</script>

<template>
  <div class="public-page">
    <div class="page-head">
      <div>
        <div class="mute" style="font-size: var(--t-sm)">shared deployment · read-only</div>
        <h1 style="font-size: var(--t-xl)">{{ id }}</h1>
        <div class="sub">
          <span v-if="detail">stack <b>{{ detail.stack }}</b></span>
          <span v-if="expiresAt" class="mute"> · link expires {{ stamp(expiresAt) }}</span>
        </div>
      </div>
      <span class="grow" />
      <span v-if="state" class="badge" :class="state.cls">{{ state.text }}</span>
    </div>

    <div v-if="fatal" class="banner failed">
      <b>{{ fatal }}</b>
      <p>Ask whoever shared it for a new link.</p>
    </div>

    <template v-else>
      <!-- ============================ details ============================ -->
      <section v-if="views.includes('details')" class="panel">
        <div class="phead"><h2 class="section">Details</h2></div>
        <ul v-if="detail" class="kvlist">
          <li><span class="k">Kind</span><span class="v">{{ sentence(detail.kind) }}</span></li>
          <li><span class="k">Orchestrator</span><span class="v">{{ detail.orchestrator ?? 'compose' }}</span></li>
          <li><span class="k">Created</span><span class="v">{{ stamp(detail.createdAt) }}</span></li>
          <li><span class="k">Updated</span><span class="v">{{ stamp(detail.updatedAt) }}</span></li>
          <li v-if="detail.asleep">
            <span class="k">Asleep</span>
            <span class="v">since {{ stamp(detail.asleep.since) }} <span class="mute">({{ detail.asleep.reason }})</span></span>
          </li>
        </ul>
        <p v-else class="mute">Details could not be loaded.</p>

        <div v-if="runtime && runtime.reachable === false" class="banner warn" style="margin-top: var(--s3)">
          <b>Docker did not answer.</b>
          <p>What is running could not be listed — not the same as nothing running.</p>
        </div>
        <table v-else-if="runtime?.containers.length" class="cards" style="margin-top: var(--s3)">
          <thead>
            <tr><th>Service</th><th>Container</th><th>State</th><th>Health</th><th v-if="runtime.containers.some((c) => c.node)">Node</th></tr>
          </thead>
          <tbody>
            <tr v-for="c in runtime.containers" :key="c.id">
              <td data-label="service">{{ c.service ?? '—' }}</td>
              <td data-label="container" class="mono">{{ c.name }}</td>
              <td data-label="state"><span :class="c.state === 'running' ? 's-ok' : 's-failed'">{{ sentence(c.state) }}</span></td>
              <td data-label="health">{{ c.health ? sentence(c.health) : '—' }}</td>
              <td v-if="runtime.containers.some((x) => x.node)" data-label="node">{{ c.node ?? '—' }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else-if="runtime" class="mute" style="margin-top: var(--s3)">Nothing is running.</p>
      </section>

      <!-- ============================ logs ============================ -->
      <section v-if="views.includes('logs')" class="panel">
        <div class="phead">
          <h2 class="section">Logs</h2>
          <span class="grow" />
          <div class="row" style="flex-wrap: wrap; gap: var(--s3)">
            <div v-if="services.length" class="field inline">
              <label for="pub-service">Service</label>
              <SelectMenu
                id="pub-service"
                v-model="service"
                label="Which service"
                :options="[{ value: '', label: 'All services' }, ...services.map((s) => ({ value: s, label: s }))]"
              />
            </div>
            <label class="check"><input v-model="timestamps" type="checkbox" /> Timestamps</label>
            <label class="check"><input v-model="following" type="checkbox" /> Follow</label>
          </div>
        </div>
        <LogViewer :rows="rows" :live="following && !!source" :copy-text="lines.join('\n')" :empty-text="following ? 'Waiting for output…' : '(no output)'" />
        <p v-if="logNote" class="hint">{{ logNote }}</p>
      </section>
    </template>

    <p class="mute" style="font-size: var(--t-sm); margin-top: var(--s4)">pstack · this page can only read.</p>
  </div>
</template>

<style scoped>
/* Full width: the shell centres the view as a card when nobody is signed in (the login page), but a
   log pane wants the whole viewport. */
.public-page {
  width: 100%;
  max-width: 1400px;
  margin: 0 auto;
  justify-self: stretch;
}
</style>
