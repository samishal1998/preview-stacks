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
import type { ConflictBody } from '../../api/types';
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

const router = useRouter();

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
            :disabled="!!pending || busy || (isShared && !forceArmed)"
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
          Something is already running on <b>{{ dep.detail.stack }}</b>.
          <InfoHint label="why actions wait">
            One action at a time per stack, and they are not queued. A teardown racing a deploy over
            the same database would corrupt it, so the second request is refused rather than delayed.
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
              <code>docker compose down -v</code> — the <code>-v</code> is what removes the volumes.
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
