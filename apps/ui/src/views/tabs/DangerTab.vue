<script setup lang="ts">
/**
 * Teardown and forgetting — everything on this page destroys something, and NOTHING else is here.
 *
 * Deploy and Verify used to live on this tab too, which meant the product's primary action was
 * filed under "Danger". They moved to the detail header (`useDeploymentActions`); this tab now
 * carries exactly what its name promises.
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
import { api, problem } from '../../api/client';
import type { CancelStackResponse, ConflictBody, ShareLink, ShareView } from '../../api/types';
import { dep, isShared, varsQuery } from '../../composables/useDeployment';
import {
  act,
  actionError,
  busy,
  conflict,
  conflictJobId,
  onConflict,
  pending,
  tokenMissing,
  varsSummary,
  whyDisabled,
} from '../../composables/useDeploymentActions';
import { loadDeployments } from '../../composables/useControlPlane';
import { toast } from '../../composables/useToasts';
import ActionButton from '../../components/ActionButton.vue';
import ConflictNote from '../../components/ConflictNote.vue';
import InfoHint from '../../components/InfoHint.vue';
import ErrorNote from '../../components/ErrorNote.vue';
import EquivalentCommand from '../../components/EquivalentCommand.vue';
import SelectMenu from '../../components/SelectMenu.vue';

const router = useRouter();

// ── share link ──────────────────────────────────────────────────────────────────────────────────
// Here rather than on Overview because it MINTS A CREDENTIAL: a URL that reads this deployment's
// logs until it expires, with no per-link revocation. The result is shown once and held in
// component state only — never in Settings, never in storage, never in a toast.
const shareDetails = ref(true);
const shareLogs = ref(true);
const shareTtl = ref('7d');
const shareLink = ref<ShareLink | null>(null);
const shareError = ref('');
const sharePending = ref(false);
const shareViews = computed<ShareView[]>(() => [
  ...(shareDetails.value ? (['details'] as const) : []),
  ...(shareLogs.value ? (['logs'] as const) : []),
]);

async function share(): Promise<void> {
  sharePending.value = true;
  shareError.value = '';
  shareLink.value = null;
  const r = await api.post<ShareLink>(`/api/deployments/${encodeURIComponent(dep.id)}/share`, {
    views: shareViews.value,
    ttl: shareTtl.value,
  });
  sharePending.value = false;
  if (r.status === 201) {
    shareLink.value = r.body;
    return;
  }
  shareError.value = problem(r, 'mint a share link');
}

async function copyShare(): Promise<void> {
  if (!shareLink.value) return;
  try {
    await navigator.clipboard.writeText(shareLink.value.url);
    toast('ok', 'Link copied.');
  } catch {
    toast('error', 'Could not reach the clipboard — select the link and copy it.');
  }
}

// ── stop everything ─────────────────────────────────────────────────────────────────────────────
// Here rather than on the job page because a stack's outstanding work is a property of the STACK,
// and the job page only knows the one job you happened to open. It is not a teardown: it stops the
// running job and drops the one queued behind it, and destroys nothing.
const cancelPending = ref(false);

async function cancelStack(): Promise<void> {
  cancelPending.value = true;
  const r = await api.post<CancelStackResponse>(
    `/api/deployments/${encodeURIComponent(dep.id)}/cancel${varsQuery.value}`,
  );
  cancelPending.value = false;
  if (!r.ok) {
    toast('error', problem(r, 'stop this stack'));
    return;
  }
  // "Nothing was outstanding" is an ANSWER, not a failure — the operator asked whether anything was
  // running and the reply is no. Saying "stopped 0 jobs" would read as a bug.
  const n = r.body.cancelled.length;
  toast(
    n ? 'ok' : 'info',
    n
      ? `Stopped ${n} job${n > 1 ? 's' : ''} on ${r.body.stack}. ${r.body.warning}`
      : `Nothing was running or queued on ${r.body.stack}.`,
  );
  void loadDeployments();
}

const downVerify = ref(true);
const forceTyped = ref('');
const forgetArmed = ref(false);

/** Exact match against the resolved stack name. Nothing looser — no trim-insensitive, no prefix. */
const forceArmed = computed(
  () => !!dep.detail?.stack && forceTyped.value === dep.detail.stack,
);

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
  actionError.value = problem(r, 'forget this deployment');
}
</script>

<template>
  <div>
    <!-- ============================ sleep ============================ -->
    <section v-if="dep.detail && !isShared" class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Sleep</h2>
      <p class="dim">
        Takes the {{ dep.detail.orchestrator === 'swarm' ? 'swarm stack' : 'compose project' }} down and
        <b>keeps its volumes and every axis</b>. The next request to any of its hostnames brings it
        back — the visitor sees “spinning up…” for the length of a deploy — and so does <b>Wake</b> in
        the header.
        <InfoHint label="what sleep runs">
          <code>{{ dep.detail.orchestrator === 'swarm' ? 'docker stack rm' : 'docker compose down' }}</code>
          — without <code>-v</code>, which is the whole difference from tearing down. A spec can
          schedule this itself with a <code>sleep:</code> block (idle / after).
        </InfoHint>
      </p>
      <div class="row" style="margin-top: var(--s3)">
        <ActionButton
          :pending="pending === 'sleep'"
          :disabled="!!pending || busy || !!whyDisabled('sleep')"
          :title="whyDisabled('sleep')"
          @click="act('sleep')"
        >
          Sleep now
        </ActionButton>
        <span v-if="dep.detail.asleep" class="mute">
          Asleep since {{ new Date(dep.detail.asleep.since).toLocaleString() }} ({{ dep.detail.asleep.reason }}).
        </span>
      </div>
    </section>

    <!-- ============================ share ============================ -->
    <section v-if="dep.detail" class="panel">
      <div class="phead">
        <h2 class="section">Share a read-only link</h2>
        <span class="grow" />
        <EquivalentCommand
          what="this link"
          method="POST"
          :path="`/api/deployments/${encodeURIComponent(dep.id)}/share`"
          :body="{ views: shareViews, ttl: shareTtl }"
        />
      </div>
      <p class="dim">
        A URL that opens <b>this deployment only</b>, for someone without an account. It carries a
        signed token that reaches exactly the views below until it expires.
        <InfoHint label="what a link can and cannot do">
          Read-only, and scoped: details and logs of this deployment, nothing else — no actions, no
          terminal, no other deployment. There is no per-link revocation; rotating the host's
          PSTACK_TOKEN invalidates every link at once, so keep the expiry short.
        </InfoHint>
      </p>
      <div class="row" style="margin-top: var(--s3); flex-wrap: wrap; gap: var(--s3)">
        <label class="check"><input v-model="shareDetails" type="checkbox" /> Details &amp; containers</label>
        <label class="check"><input v-model="shareLogs" type="checkbox" /> Logs</label>
        <div class="field inline">
          <label for="share-ttl">Expires</label>
          <SelectMenu
            id="share-ttl"
            v-model="shareTtl"
            label="How long the link works"
            :options="[
              { value: '1h', label: 'In 1 hour' },
              { value: '24h', label: 'In 24 hours' },
              { value: '7d', label: 'In 7 days' },
              { value: '30d', label: 'In 30 days' },
            ]"
          />
        </div>
        <ActionButton
          variant="primary"
          :pending="sharePending"
          :disabled="sharePending || shareViews.length === 0"
          :title="shareViews.length === 0 ? 'Pick at least one view.' : undefined"
          @click="share"
        >
          Create link
        </ActionButton>
      </div>
      <div v-if="shareLink" class="banner ok" style="margin-top: var(--s3)">
        <b>Your link — copy it now.</b>
        <p>
          Opens {{ shareLink.views.join(' and ') }} of <b>{{ dep.id }}</b> until
          {{ new Date(shareLink.expiresAt).toLocaleString() }}. It is shown once and stored nowhere.
        </p>
        <pre class="code" style="white-space: pre-wrap; word-break: break-all">{{ shareLink.url }}</pre>
        <div class="row">
          <button @click="copyShare">Copy</button>
          <a :href="shareLink.url" target="_blank" rel="noopener">Open ↗</a>
          <button @click="shareLink = null">Done</button>
        </div>
      </div>
      <ErrorNote v-if="shareError" :text="shareError" title="No link was created." />
    </section>

    <section class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Tear down</h2>

      <p v-if="!dep.detail" class="mute">
        Unavailable until this deployment's variables are filled in — without them its stack has no
        name to act on. Set them under
        <RouterLink :to="`/deployments/${encodeURIComponent(dep.id)}/config`">
          Config &amp; variables </RouterLink
        >first.
      </p>

      <template v-else>
        <div v-if="tokenMissing" class="banner warn">
          <b>No access token set.</b>
          <p>
            Every action below will be refused. Add your token under
            <RouterLink to="/settings">Settings</RouterLink>.
          </p>
        </div>

        <p class="dim">
          Stops and removes this deployment's containers, networks and volumes.
          {{ varsSummary }}
          <RouterLink :to="`/deployments/${encodeURIComponent(dep.id)}/config`">change</RouterLink>
        </p>

        <div class="row" style="margin-top: var(--s3)">
          <ActionButton
            variant="danger"
            :pending="pending === 'down'"
            :disabled="!!pending || (isShared && !forceArmed)"
            :title="whyDisabled('down', forceArmed)"
            @click="act('down', { verify: downVerify, force: isShared && forceArmed })"
          >
            {{ isShared ? 'Tear down (force)' : 'Tear down' }}
          </ActionButton>
          <label class="check">
            <input v-model="downVerify" type="checkbox" />
            Check for leftovers afterwards
          </label>
        </div>

        <p v-if="busy" class="hint">
          Something is already running on <b>{{ dep.detail.stack }}</b> — tearing down
          <b>cancels it first</b>.
          <InfoHint label="what happens to the job that is running">
            One job runs per stack at a time; a teardown racing a deploy over the same database
            would corrupt it. A second <em>deploy</em> therefore waits its turn (and a third
            replaces the one waiting). A teardown does not wait: it stops the running job, drops
            anything queued behind it, and runs. Whatever the stopped job had already done is
            <b>not</b> undone — which is what tearing down is about to deal with anyway.
          </InfoHint>
        </p>
        <p v-if="!downVerify" class="hint">
          Nothing will check that teardown actually finished. A teardown that silently half-worked is
          the exact failure this tool exists to catch — run <b>Verify</b> yourself afterwards.
        </p>

        <!--
          Gate 1 of 2. The typed name is the point: a checkbox is one stray click, typing the stack
          name is a decision.
        -->
        <div v-if="isShared" class="banner leaked">
          <b>Shared — read this before tearing down.</b>
          <p>
            Tearing this down <b>deletes its stored data</b>: the TLS certificates, the shared
            database or queue, admin credentials. Nothing here recreates them, so
            <b>every preview on this host breaks</b> until someone rebuilds it by hand. The button is
            the same one that is routine on a single preview.
            <InfoHint label="what tearing down runs">
              <code>{{ dep.detail.orchestrator === 'swarm' ? 'docker stack rm, then docker volume rm on its volumes' : 'docker compose down -v' }}</code>
              — removing the volumes is what makes this different from sleeping.
            </InfoHint>
          </p>
          <div class="field" style="margin-top: var(--s3); max-width: 380px">
            <label :for="`confirm-${dep.id}`">
              Type the stack name <b>{{ dep.detail.stack }}</b> to confirm
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
          <p v-if="forceTyped && !forceArmed" class="s-failed">
            That does not match, so tearing down stays disabled.
          </p>
        </div>

        <!--
          Stopping is not tearing down, so it sits below the teardown control rather than beside it:
          nothing is destroyed, nothing already done is undone, and a stack left half-deployed is
          the point — you stop a wrong deploy so you can start the right one.
        -->
        <div class="row" style="margin-top: var(--s4)">
          <ActionButton
            variant="danger"
            :pending="cancelPending"
            :disabled="cancelPending"
            confirm="Stop everything? Nothing is undone."
            title="Cancels the job running on this stack and drops the one queued behind it. Destroys nothing."
            @run="cancelStack"
          >
            Stop everything on this stack
          </ActionButton>
          <span class="mute">
            The running job and the one waiting behind it, in one call — nothing is torn down, and
            whatever the running job already did stays done.
          </span>
        </div>

        <ErrorNote v-if="actionError" :text="actionError" title="The action was refused." />
        <ConflictNote v-if="conflict" :conflict="conflict" :job-id="conflictJobId" />
      </template>
    </section>

    <!-- ============================ forget ============================ -->
    <section class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Forget this deployment</h2>

      <p v-if="!dep.detail" class="mute">
        Unavailable until the variables are filled in: this stack's containers are counted before the
        record can be forgotten, and that needs its name.
      </p>
      <template v-else>
        <p class="dim">
          This removes the <em>record only</em> — <b>it never tears anything down</b>. Afterwards
          nothing here knows how to remove whatever this deployment created.
          <InfoHint label="when forgetting is refused">
            Refused while any container for this stack still exists — and refused just as firmly when
            docker cannot be reached, because “cannot confirm” is not evidence of absence. What is
            deleted is the stored spec, compose file and metadata.
          </InfoHint>
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
