<script setup lang="ts">
/**
 * One job: its live log while it runs, then the step table that says what actually happened.
 *
 * The log streams over `EventSource`. The server replays the whole buffered history first and then
 * sends live events, so attaching late still shows the beginning — no polling, no gap to reconcile.
 * A final `{done:true,state}` ends the stream; we close the source then and fetch the job once more
 * to pick up `outcome`, which only exists after the job finishes.
 *
 * A QUEUED JOB HAS A STREAM TOO, and it is open and silent until the job dispatches. That is the
 * one case where "no events yet" is not a broken connection, so the page says what it is waiting
 * for instead of showing an empty log: the panel reads "waiting to start", and a banner above it
 * says WHY — behind its own stack, or behind the host's job cap. The first line to arrive IS the
 * dispatch, and nothing else would re-fetch, so it triggers one `load()`; without that the badge
 * would still read "Queued" while output scrolled past underneath it.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { api, problem } from '../api/client';
import type { Job, LogEvent } from '../api/types';
import { state } from '../composables/useControlPlane';
import { isTerminal, supersededBy, waitReason } from '../composables/useJobQueue';
import { leakedAxes, countUnverifiable } from '../composables/useSteps';
import { actionLabel, stamp, took } from '../composables/useFormat';
import LogViewer from '../components/LogViewer.vue';
import type { LogRow } from '../components/log-viewer-types';
import StepList from '../components/StepList.vue';
import StateBadge from '../components/StateBadge.vue';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import { toast } from '../composables/useToasts';

const props = defineProps<{ jobId: string }>();

const job = ref<Job | null>(null);
const lines = ref<LogEvent[]>([]);
const error = ref('');
const loading = ref(true);
const streaming = ref(false);

/** Job events → viewer rows. The date repeats for hundreds of lines, so it appears once per DAY as
 *  a separator and each row carries only the clock; the full stamp survives in the tooltip. */
const logRows = computed<LogRow[]>(() => {
  const out: LogRow[] = [];
  let lastDay = '';
  for (const l of lines.value) {
    const d = new Date(l.at);
    const day = d.toLocaleDateString('sv');
    if (day !== lastDay) {
      out.push({ key: `day-${day}-${l.seq}`, separator: true, text: day });
      lastDay = day;
    }
    out.push({
      key: l.seq,
      gutter: d.toTimeString().slice(0, 8),
      gutterTitle: stamp(l.at),
      text: l.message,
      tone: l.level,
    });
  }
  return out;
});

let source: EventSource | null = null;

/** Terminal is forever. NEVER `state !== 'running'` — that is true of a job that has not started. */
const terminal = computed(() => !!job.value && isTerminal(job.value.state));
const queued = computed(() => job.value?.state === 'queued');

/**
 * Why this job is still waiting, and which job replaced it — both read the shell's polled job list,
 * which is loaded on every route. On a deep link before the first poll the list is empty and both
 * fall back to saying nothing rather than guessing.
 */
const wait = computed(() => (job.value ? waitReason(job.value, state.jobs) : null));
const replacement = computed(() => (job.value ? supersededBy(job.value, state.jobs) : null));

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
  /*
   * The stream REPLAYS the whole buffer before going live, and `load()` has already seeded `lines`
   * from the same buffer — so every line the job had produced before the page opened was rendered
   * twice, with the count to match. Clearing here is the honest fix: the replay is complete by
   * definition, so the seed is only ever the placeholder that keeps a finished job from flashing
   * empty.
   */
  lines.value = [];
  // A queued job's stream carries nothing until it dispatches, and the first line to arrive is that
  // dispatch. Re-read the job once when it does, or the header keeps saying "Queued".
  let awaitingDispatch = queued.value;
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
    if (awaitingDispatch) {
      awaitingDispatch = false;
      void load(); // it started: pick up `running` and the `startedAt` it now has
    }
    lines.value.push(payload as LogEvent);
  };

  // An error here is usually the job having ended between the fetch and the connect, or the API
  // becoming unreachable. Either way stop rather than let the browser reconnect forever.
  source.onerror = () => {
    closeStream();
    void load();
  };
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
    // Queued as well as running: the stream is what tells this page the job began.
    if (loaded && !isTerminal(loaded.state)) stream();
  },
  { immediate: true },
);

const cancelling = ref(false);

/**
 * Stop the job.
 *
 * The stream is deliberately left open: the server writes the cancellation into the SAME log the
 * page is already watching, so the operator sees the run stop where it stopped instead of watching a
 * page that has gone quiet. `load()` afterwards picks up the final state and `cancelledBy`.
 */
async function cancel(): Promise<void> {
  cancelling.value = true;
  const r = await api.post<{ warning?: string }>(
    `/api/jobs/${encodeURIComponent(props.jobId)}/cancel`,
  );
  cancelling.value = false;
  if (!r.ok) {
    // 409 means it finished on its own between the click and the request — not an error worth an
    // alarm, just a stale page.
    toast(r.status === 409 ? 'info' : 'error', problem(r, 'stop this job'));
    void load();
    return;
  }
  toast('ok', r.body.warning ?? 'Stopped.');
  void load();
}

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
            <code>{{ job.stack }}</code>
            <template v-if="job.startedAt"> · started {{ stamp(job.startedAt) }}</template>
            <template v-else-if="queued"> · not started yet</template>
            <template v-else> · never started</template>
            <template v-if="job.startedAt && job.endedAt">
              · took {{ took(job.startedAt, job.endedAt) }}
            </template>
          </template>
          <template v-else>{{ jobId }}</template>
        </div>
      </div>
      <span class="grow" />
      <StateBadge v-if="job" :state="job.state" />
      <!--
        Stopping is destructive in the way that matters here: it does not undo, so the confirmation
        says what will be left behind rather than asking "are you sure".
      -->
      <ActionButton
        v-if="job && !terminal"
        variant="danger"
        :pending="cancelling"
        :confirm="queued ? 'Drop it? It never ran.' : 'Stop it? Nothing is undone.'"
        :title="
          queued
            ? 'It has not started, so there is nothing left behind to clean up.'
            : 'Kills the command in flight. Anything already created or destroyed stays that way.'
        "
        @run="cancel"
      >
        {{ queued ? 'Drop' : 'Stop' }}
      </ActionButton>
    </div>

    <!--
      A queued job and a broken page look identical — an open connection, an empty log, nothing
      moving. So this says which it is, and WHY it waits, because the two waits have different
      fixes: behind its own stack, wait or stop the job ahead; behind the host's cap, raise
      PSTACK_MAX_JOBS or deploy fewer things at once. Neutral chrome on purpose — waiting is the
      system working, not an alarm.
    -->
    <section v-if="queued" class="panel">
      <b>Waiting to start — nothing has run yet.</b>
      <p v-if="wait?.kind === 'stack'" class="mute">
        <RouterLink :to="`/jobs/${encodeURIComponent(wait.blocker.id)}`">{{
          actionLabel(wait.blocker.action)
        }}</RouterLink>
        is already running on <code>{{ job?.stack }}</code
        >. One job runs per stack at a time — a teardown racing a deploy over the same database is
        the failure that rule exists to prevent — so this one starts when that one ends.
      </p>
      <p v-else-if="wait?.kind === 'slot'" class="mute">
        Nothing is running on <code>{{ job?.stack }}</code
        >, so the wait is host-wide: {{ wait.running }} jobs hold a slot across every stack, and the
        host runs at most <code>PSTACK_MAX_JOBS</code> at once (4 unless this server was told
        otherwise). This one starts as soon as a slot frees.
      </p>
      <p v-else class="mute">
        It starts once its own stack is free and the host has a spare job slot.
      </p>
      <p class="mute">Dropping it now costs nothing — it has not touched anything yet.</p>
    </section>

    <!--
      The mirror image of the cancelled banner below, and the reason `superseded` is its own state:
      a stopped job leaves partial state to hunt for, a superseded one cannot, because it never ran.
    -->
    <section v-if="job?.state === 'superseded'" class="panel">
      <b>Superseded — this job never ran.</b>
      <p class="mute">
        <template v-if="replacement">
          A newer
          <RouterLink :to="`/jobs/${encodeURIComponent(replacement.id)}`">{{
            actionLabel(replacement.action).toLowerCase()
          }}</RouterLink>
          for <code>{{ job.stack }}</code> replaced it while it was queued.
        </template>
        <template v-else>
          A newer job for <code>{{ job.stack }}</code> replaced it while it was queued.
        </template>
        The queue is one deep, so a burst of pushes runs the first deploy and then exactly one more
        carrying the newest spec. Nothing was created or destroyed here — there is no partial state
        to go looking for, and nothing to verify.
      </p>
    </section>

    <!--
      Not a badge's worth of information. A cancelled job is the one state where the record is
      complete and the INFRASTRUCTURE is not, and the next step is a person's to take.
    -->
    <section v-if="job?.state === 'cancelled'" class="panel banner-panel warn">
      <b>Stopped by {{ job.cancelledBy ?? 'an operator' }} — nothing was undone.</b>
      <p>
        Whatever this job had already created or destroyed is still that way. Run
        <b>Verify</b> on the deployment to see what actually exists.
      </p>
    </section>

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
      </div>

      <!-- Time-of-day gutter with a date separator when the day flips — the full 19-character
           timestamp per line is what used to wrap every entry onto five or six lines. -->
      <LogViewer
        :rows="logRows"
        :live="streaming"
        :empty-text="
          loading
            ? 'Loading…'
            : queued
              ? 'Waiting to start — there is no output until it does.'
              : 'No output.'
        "
      >
        <template #actions>
          <button v-if="!streaming && job && !terminal" class="ghost sm" @click="stream">
            Reconnect
          </button>
        </template>
      </LogViewer>
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
