<script setup lang="ts">
/**
 * Lifecycle actions, and forgetting the record. Everything on this page destroys something.
 *
 * The two guards that matter:
 *
 *  1. TYPED CONFIRMATION FOR A SHARED `down`. A checkbox is one stray click; typing a name is a
 *     decision. The typed string is the resolved STACK NAME — not the deployment id — because the
 *     stack is the actual blast target, it is what the server's own 409 refusal names, and it is
 *     what `compose down -v` will be pointed at. The server holds the authoritative guard
 *     (`down()` refuses without `force`, and the API answers 409 before starting a job), so this
 *     is defence in depth, not the only barrier.
 *
 *  2. FORGETTING IS NOT TEARING DOWN. `DELETE` removes the record and never touches a container.
 *     The server refuses with 409 while any container exists — and refuses just as firmly when
 *     docker did not answer, because "cannot confirm" is not evidence of absence.
 */
import { computed, ref } from 'vue';
import { useRouter } from 'vue-router';
import { api, classifyConflict, problem, type Conflict } from '../../api/client';
import type { ActionResponse, ConflictBody, JobsResponse } from '../../api/types';
import { dep, isShared, varsQuery } from '../../composables/useDeployment';
import { loadDeployments, state } from '../../composables/useControlPlane';
import { settings } from '../../composables/useSettings';
import { toast } from '../../composables/useToasts';
import ActionButton from '../../components/ActionButton.vue';
import ConflictNote from '../../components/ConflictNote.vue';
import ErrorNote from '../../components/ErrorNote.vue';

const router = useRouter();

const pending = ref<'' | 'up' | 'verify' | 'down' | 'forget'>('');
const actionError = ref('');
const conflict = ref<Conflict | null>(null);
const conflictJobId = ref('');
const downVerify = ref(true);
const forceTyped = ref('');
const forgetArmed = ref(false);

/** Exact match against the resolved stack name. Nothing looser — no trim-insensitive, no prefix. */
const forceArmed = computed(
  () => !!dep.detail?.stack && forceTyped.value === dep.detail.stack,
);

const tokenMissing = computed(() => state.health?.authEnforced === true && !settings.token);
const busy = computed(() => dep.detail?.busy === true);

/** A disabled control must say why. This is the text that answers it. */
function whyDisabled(action: 'up' | 'verify' | 'down'): string | undefined {
  if (!dep.detail) return 'The spec has not resolved — the server cannot compute the stack name.';
  if (busy.value) return `A job is already in flight for ${dep.detail.stack}.`;
  if (action === 'down' && isShared.value && !forceArmed.value) {
    return `Type the stack name "${dep.detail.stack}" below to confirm a shared teardown.`;
  }
  return undefined;
}

async function act(action: 'up' | 'verify' | 'down'): Promise<void> {
  if (!dep.detail) return;
  pending.value = action;
  actionError.value = '';
  conflict.value = null;

  const body =
    action === 'down'
      ? // `force` is only ever true for a shared deployment whose stack name was typed out in full.
        { verify: downVerify.value, force: isShared.value && forceArmed.value }
      : {};

  const r = await api.post<ActionResponse & ConflictBody>(
    `/api/deployments/${encodeURIComponent(dep.id)}/${action}${varsQuery.value}`,
    body,
  );
  pending.value = '';

  if (r.status === 202) {
    const job = r.body.job;
    if (job?.id) {
      toast('info', `${action} started on ${job.stack}`, {
        to: `/jobs/${encodeURIComponent(job.id)}`,
        toLabel: 'Follow',
      });
      void loadDeployments();
      void router.push(`/jobs/${encodeURIComponent(job.id)}`);
      return;
    }
    actionError.value = '202 accepted, but the response carried no job id — nothing to follow.';
    return;
  }
  if (r.status === 409) return onConflict(r.body);

  actionError.value = problem(r, `POST ${action}`);
  toast('error', `${action} was refused.`);
}

async function forget(): Promise<void> {
  if (!forgetArmed.value) return;
  pending.value = 'forget';
  actionError.value = '';
  conflict.value = null;

  const r = await api.del<ConflictBody>(
    `/api/deployments/${encodeURIComponent(dep.id)}${varsQuery.value}`,
  );
  pending.value = '';

  if (r.ok) {
    toast('ok', `Forgot ${dep.id}. Nothing was torn down.`);
    void loadDeployments();
    void router.push('/deployments');
    return;
  }
  if (r.status === 409) return onConflict(r.body);
  actionError.value = problem(r, 'DELETE deployment');
}

/**
 * Classify on payload SHAPE (in `client.ts`), never on message text — then look for a running job
 * ONLY to offer a link. "A job is in flight" and "docker did not answer" are shape-identical, so
 * neither is claimed; the server's message is printed verbatim either way. Never retry.
 */
async function onConflict(body: ConflictBody): Promise<void> {
  const c = classifyConflict(body);
  conflict.value = c;
  conflictJobId.value = '';
  if (c.kind === 'shared') forceTyped.value = '';

  const stack = c.stack || dep.detail?.stack;
  if (c.kind !== 'other' || !stack) return;
  const jl = await api.get<JobsResponse>('/api/jobs');
  const live = (jl.body.jobs ?? []).find((j) => j.state === 'running' && j.stack === stack);
  if (live) conflictJobId.value = live.id;
}
</script>

<template>
  <div>
    <section class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Lifecycle</h2>

      <p v-if="!dep.detail" class="mute">
        Actions are unavailable until the spec resolves — the server cannot act on a deployment
        whose stack name it cannot compute. Fix the variables under
        <RouterLink :to="`/deployments/${encodeURIComponent(dep.id)}/config`">
          Config &amp; variables </RouterLink
        >first.
      </p>

      <template v-else>
        <div v-if="tokenMissing" class="banner warn">
          <b>No token set.</b>
          <p>
            Every action below is a mutating request and will be refused with <code>401</code>. Add
            <code>PSTACK_TOKEN</code> under <RouterLink to="/settings">Settings</RouterLink>.
          </p>
        </div>

        <div class="row">
          <ActionButton
            variant="primary"
            :pending="pending === 'up'"
            :disabled="!!pending || busy"
            :title="whyDisabled('up')"
            @click="act('up')"
          >
            up
          </ActionButton>
          <ActionButton
            :pending="pending === 'verify'"
            :disabled="!!pending || busy"
            :title="whyDisabled('verify')"
            @click="act('verify')"
          >
            verify
          </ActionButton>
          <ActionButton
            variant="danger"
            :pending="pending === 'down'"
            :disabled="!!pending || busy || (isShared && !forceArmed)"
            :title="whyDisabled('down')"
            @click="act('down')"
          >
            {{ isShared ? 'down (force)' : 'down' }}
          </ActionButton>
          <span class="grow" />
          <RouterLink :to="`/submit/${encodeURIComponent(dep.id)}`">replace spec →</RouterLink>
        </div>

        <p v-if="busy" class="hint">
          A job is in flight for <b class="mono">{{ dep.detail.stack }}</b
          >. One job per stack, deliberately not queued — a <code>down</code> racing an
          <code>up</code> over the same database branch is corruption, not contention.
        </p>
        <p class="hint">
          Sent with <span class="mono">{{ varsQuery || 'no request variables' }}</span
          >.
          <RouterLink :to="`/deployments/${encodeURIComponent(dep.id)}/config`">change</RouterLink>
        </p>

        <div class="row" style="margin-top: var(--s3)">
          <label class="check">
            <input v-model="downVerify" type="checkbox" />
            run <code>verify</code> after <code>down</code>
          </label>
        </div>
        <p v-if="!downVerify" class="hint">
          Teardown will not check for leaks. A teardown that silently half-worked is the exact
          failure this tool exists to catch — run <code>verify</code> yourself afterwards.
        </p>

        <!--
          Gate 1 of 2. The typed name is the point: a checkbox is one stray click, typing the stack
          name is a decision.
        -->
        <div v-if="isShared" class="banner leaked">
          <b>SHARED — read this before tearing down.</b>
          <p>
            <code>down</code> runs <code>docker compose down -v</code>, and <code>-v</code> removes
            <b>volumes</b>. On a shared deployment that destroys state every tenant depends on — the
            TLS certificate store, the shared database or queue, admin credentials. Nothing here
            recreates them: <b>every preview on this host breaks</b> until someone rebuilds the
            singleton by hand. It is the identical verb that is routine on a preview.
          </p>
          <div class="field" style="margin-top: var(--s3); max-width: 380px">
            <label :for="`confirm-${dep.id}`">
              Type the stack name <b class="mono">{{ dep.detail.stack }}</b> to send
              <code>force: true</code>
            </label>
            <input
              :id="`confirm-${dep.id}`"
              v-model.trim="forceTyped"
              type="text"
              :placeholder="dep.detail.stack"
              spellcheck="false"
              autocomplete="off"
            />
          </div>
          <p v-if="forceTyped && !forceArmed" class="mono">
            does not match — <code>down</code> stays disabled
          </p>
        </div>

        <ErrorNote v-if="actionError" :text="actionError" title="The action was refused." />
        <ConflictNote v-if="conflict" :conflict="conflict" :job-id="conflictJobId" />
      </template>
    </section>

    <!-- ============================ forget ============================ -->
    <section class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Forget this deployment</h2>

      <p v-if="!dep.detail" class="mute">
        Unavailable until the spec resolves — the server counts this stack's containers before it
        will forget the record, and it cannot name the stack without its variables.
      </p>
      <template v-else>
        <p class="dim" style="font-size: var(--t-sm)">
          <code>DELETE</code> removes the <em>record</em> — the stored <code>spec.yml</code>,
          <code>compose.yml</code> and <code>meta.json</code>. <b>It never tears anything down.</b>
          The server refuses with <code>409</code> while any container for this stack still exists,
          and refuses just as firmly when docker did not answer: “cannot confirm” is not evidence of
          absence.
        </p>
        <p class="hint">
          After forgetting, this deployment disappears from the control plane. If a resource one of
          its axes created is still alive, nothing here knows how to remove it any more.
        </p>
        <div class="row" style="margin-top: var(--s3)">
          <label class="check">
            <input v-model="forgetArmed" type="checkbox" />
            I have torn this down and want the record removed
          </label>
          <ActionButton
            variant="danger"
            :pending="pending === 'forget'"
            :disabled="!forgetArmed || !!pending"
            :title="forgetArmed ? undefined : 'Confirm you have torn this down first.'"
            @click="forget"
          >
            Forget
          </ActionButton>
        </div>
      </template>
    </section>
  </div>
</template>
