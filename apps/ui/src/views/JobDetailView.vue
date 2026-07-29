<script setup lang="ts">
/**
 * One job: its live log while it runs, then the step table that says what actually happened.
 *
 * The log streams over `EventSource`. The server replays the whole buffered history first and then
 * sends live events, so attaching late still shows the beginning — no polling, no gap to reconcile.
 * A final `{done:true,state}` ends the stream; we close the source then and fetch the job once more
 * to pick up `outcome`, which only exists after the job finishes.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { api, problem } from '../api/client';
import type { Job, LogEvent } from '../api/types';
import { leakedAxes, countUnverifiable } from '../composables/useSteps';
import { actionLabel, stamp, took } from '../composables/useFormat';
import StepList from '../components/StepList.vue';
import StateBadge from '../components/StateBadge.vue';
import ErrorNote from '../components/ErrorNote.vue';

const props = defineProps<{ jobId: string }>();

const job = ref<Job | null>(null);
const lines = ref<LogEvent[]>([]);
const error = ref('');
const loading = ref(true);
const streaming = ref(false);
const follow = ref(true);
const logEl = ref<HTMLElement | null>(null);

let source: EventSource | null = null;

const leaks = computed(() => (job.value?.outcome ? leakedAxes(job.value.outcome.steps) : []));
const unverifiable = computed(() =>
  job.value?.outcome ? countUnverifiable(job.value.outcome.steps) : 0,
);

/** Returns the job it loaded, so callers need not re-read the ref — see the note at the watcher. */
async function load(): Promise<Job | null> {
  const r = await api.get<{ job: Job }>(`/api/jobs/${encodeURIComponent(props.jobId)}`);
  loading.value = false;
  if (!r.ok) {
    error.value = problem(r, 'load job');
    return null;
  }
  error.value = '';
  job.value = r.body.job;
  // The job carries its buffered log too. Seed from it so a finished job renders instantly without
  // opening a stream that would only replay and close.
  if (r.body.job.log?.length && lines.value.length === 0) lines.value = [...r.body.job.log];
  return r.body.job;
}

function closeStream(): void {
  source?.close();
  source = null;
  streaming.value = false;
}

function stream(): void {
  closeStream();
  // No auth header: EventSource cannot send one. Reads are unauthenticated by design, which is
  // exactly why the log is a GET.
  source = new EventSource(api.url(`/api/jobs/${encodeURIComponent(props.jobId)}/stream`));
  streaming.value = true;

  source.onmessage = (ev) => {
    let payload: unknown;
    try {
      payload = JSON.parse(ev.data as string);
    } catch {
      return; // a frame we cannot parse is not worth tearing the stream down for
    }
    const done = payload as { done?: boolean };
    if (done.done) {
      closeStream();
      void load(); // `outcome` only exists once the job has ended
      return;
    }
    lines.value.push(payload as LogEvent);
    if (follow.value) queueMicrotask(scrollToEnd);
  };

  // An error here is usually the job having ended between the fetch and the connect, or the API
  // becoming unreachable. Either way stop rather than let the browser reconnect forever.
  source.onerror = () => {
    closeStream();
    void load();
  };
}

function scrollToEnd(): void {
  const el = logEl.value;
  if (el) el.scrollTop = el.scrollHeight;
}

watch(
  () => props.jobId,
  async () => {
    lines.value = [];
    job.value = null;
    loading.value = true;
    // Use what `load` returned rather than re-reading the ref: control-flow analysis still has
    // `job.value` narrowed to the `null` assigned above, because it cannot see that an awaited
    // call reassigned it.
    const loaded = await load();
    if (loaded && loaded.state === 'running') stream();
  },
  { immediate: true },
);

onBeforeUnmount(closeStream);
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>
          <RouterLink to="/jobs" class="mute">Jobs</RouterLink>
          <span class="mute"> / </span>{{ job ? actionLabel(job.action) : '…' }}
        </h1>
        <div class="sub">
          <template v-if="job">
            <code>{{ job.stack }}</code> · started {{ stamp(job.startedAt) }}
            <template v-if="job.endedAt"> · took {{ took(job.startedAt, job.endedAt) }}</template>
          </template>
          <template v-else>{{ jobId }}</template>
        </div>
      </div>
      <StateBadge v-if="job" :state="job.state" />
    </div>

    <ErrorNote v-if="error" :text="error" />

    <!--
      Leaked is called out above everything else. It means a resource survived teardown and nothing
      else is going to clean it up — the one outcome that needs a human.
    -->
    <section v-if="leaks.length" class="panel leaked-banner">
      <strong>Leaked:</strong> {{ leaks.join(', ') }}
      <div class="mute">
        These axes' <code>assert_gone</code> failed, so the resources are still present. They will
        not be retried — tear them down by hand, then re-run <code>verify</code>.
      </div>
    </section>

    <section v-if="job?.error" class="panel">
      <strong class="bad">The job crashed</strong>
      <pre class="pre">{{ job.error }}</pre>
    </section>

    <section class="panel">
      <div class="phead">
        <h2 class="phead-title">Log</h2>
        <div class="row" style="gap: 8px">
          <span v-if="streaming" class="mute pulse">streaming…</span>
          <label class="row" style="gap: 6px">
            <input v-model="follow" type="checkbox" />
            <span class="mute">Follow</span>
          </label>
          <button
            v-if="!streaming && job?.state === 'running'"
            class="btn"
            @click="stream"
          >
            Reconnect
          </button>
        </div>
      </div>

      <div ref="logEl" class="logbox" role="log" aria-live="polite">
        <div v-if="loading" class="mute">Loading…</div>
        <div v-else-if="!lines.length" class="mute">No output.</div>
        <div v-for="l in lines" :key="l.seq" class="logline" :class="l.level">
          <span class="mute">{{ stamp(l.at) }}</span> {{ l.message }}
        </div>
      </div>
    </section>

    <section v-if="job?.outcome" class="panel">
      <div class="phead">
        <h2 class="phead-title">Steps</h2>
        <span v-if="unverifiable" class="mute">
          {{ unverifiable }} unverifiable — no <code>assert_gone</code>, so nothing was checked
        </span>
      </div>
      <StepList :steps="job.outcome.steps" />
    </section>
  </div>
</template>
