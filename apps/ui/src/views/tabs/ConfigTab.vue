<script setup lang="ts">
/**
 * Variables (what we SEND) and declared env (what the spec DECLARES). Two different things, on
 * purpose, and the tab explains the difference rather than merging them into one table.
 */
import { computed } from 'vue';
import { applyVars, dep, persistVars, row } from '../../composables/useDeployment';
import InfoHint from '../../components/InfoHint.vue';

import { conflictingVars } from '../../composables/useVars';
import VarEditor from '../../components/VarEditor.vue';
import HelpModal from '../../components/HelpModal.vue';

/** A count, not the raw query string — `?PR=7&REGION=eu` beside a button is machine talk. */
const varCount = computed(() => Object.keys(dep.vars ?? {}).length);

const storedVars = computed(() => row.value?.vars);
const storedList = computed(() => Object.entries(storedVars.value ?? {}));
const clashes = computed(() => conflictingVars(dep.vars, storedVars.value));
</script>

<template>
  <div>
    <section class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Request variables</h2>

      <!--
        THE safety rule on this page. A spec interpolates `${VAR}` exactly once, at resolve time,
        and every route that resolves a deployment layers the request's query parameters over the
        server's environment. So `up` sent with PR=7 and a later `down` sent without it resolve to
        two DIFFERENT stacks, and the teardown quietly misses everything the deploy created.

        Keeping the pairs in this browser is what holds them matched here. It is a convenience, not
        a guarantee — a different browser, a CI job or the CLI must pass the same ones itself —
        which is why this warning stays visible rather than being dismissible.
      -->
      <p class="hint">
        Remembered in this browser. Another browser, a CI job or the command line supplies its own.
        <HelpModal title="Why the same variables have to be used for both">
          <p>
            These variables are part of how this deployment's name is worked out. Deploy with one
            value and tear down with another and the two commands are pointing at
            <b>different things</b> — the teardown finishes successfully having removed nothing, and
            the original is still running.
          </p>
          <p>
            They are kept per deployment in this browser only, which is what keeps them matched
            <em>here</em>. Nothing carries them to anywhere else.
          </p>
        </HelpModal>
      </p>

      <VarEditor v-model="dep.vars" @change="persistVars" />

      <div class="row" style="margin-top: var(--s3)">
        <button class="primary" @click="applyVars">Apply &amp; reload</button>
        <span class="mute" style="font-size: var(--t-xs)">
          {{ varCount ? `${varCount} variable${varCount === 1 ? '' : 's'}` : 'no variables' }}
        </span>
      </div>

      <p class="hint">
        Add any variable you need — the list below is what the spec declares, not everything it
        uses.
        <InfoHint label="why the two differ">
          A spec can consume a variable without declaring it, for instance by building its stack
          name out of one. If a variable is missing, the error you get when the spec fails to
          resolve names it exactly — that message is the authoritative list.
        </InfoHint>
      </p>
    </section>

    <!--
      Newer servers store variables WITH the deployment, which makes `down` self-describing — the
      strictly better arrangement. It does not make the warning above obsolete: a request variable
      still OVERRIDES a stored one, so a different value typed here retargets the stack.
    -->
    <section v-if="storedList.length" class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Stored on the server</h2>
      <p class="dim" style="font-size: var(--t-sm)">
        Kept with the deployment on the server, so deploying and tearing down agree without anyone
        passing anything. Anything set above overrides these.
      </p>
      <ul class="kvlist" style="margin-top: var(--s3)">
        <li v-for="[k, v] in storedList" :key="k">
          <span class="k"><b>{{ k }}</b></span>
          <span class="v mono">{{ v }}</span>
        </li>
      </ul>

      <div v-if="clashes.length" class="banner warn">
        <b>Your request variables disagree with the stored ones:</b>
        <b>{{ clashes.join(', ') }}</b
        >.
        <p>
          Both resolve, so nothing will error — they simply resolve to <em>different stacks</em>.
          That is the dangerous shape: an action sent from this page would target one stack while
          the CLI, CI, or another browser targets the other. Clear the override above unless you
          mean it.
        </p>
      </div>
    </section>

    <section class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">
        Declared env <span class="mute">(after interpolation)</span>
      </h2>

      <p v-if="!dep.detail" class="mute">Unavailable until the spec resolves.</p>
      <template v-else>
        <table class="cards">
          <thead>
            <tr>
              <th>key</th>
              <th>value</th>
              <th>visibility</th>
              <th>length</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in dep.detail.env" :key="e.key">
              <td class="name" data-label="key">{{ e.key }}</td>
              <td class="name" :class="{ masked: e.visibility === 'masked' }" data-label="value">
                {{ e.value }}
              </td>
              <td data-label="visibility">
                <span class="badge" :class="e.visibility === 'masked' ? 'warn' : ''">
                  {{ e.visibility }}
                </span>
              </td>
              <!--
                `length` answers "did it get set at all?" without the value. length 0 on a masked
                var is the interesting case: declared, never set — usually a hook about to fail on
                an empty credential.
              -->
              <td class="dim" data-label="length">
                {{ e.length }} chars
                <span v-if="e.length === 0" class="badge warn" style="margin-left: 6px">never set</span>
              </td>
            </tr>
            <tr v-if="!dep.detail.env.length">
              <td colspan="4" class="mute">This spec declares no <code>env:</code> block.</td>
            </tr>
          </tbody>
        </table>

        <!--
          Masked is NOT "hidden pending a click". The plaintext was never in the response, so there
          is nothing here to reveal — a reveal control would be a lie, and building one would mean
          sending secrets into a browser tab, and from there into screenshots and support tickets.
          Deny-by-default by name, decided on the host.
        -->
        <p class="hint">
          A <b>masked</b> value was redacted on the host because its name reads as a secret. The
          real value was never sent to this page, so there is <b>nothing to reveal</b> — only its
          length is shown. Read it on the host if you need it.
        </p>
      </template>
    </section>
  </div>
</template>
