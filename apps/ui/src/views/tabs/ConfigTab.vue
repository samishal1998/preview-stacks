<script setup lang="ts">
/**
 * Variables (what we SEND) and declared env (what the spec DECLARES). Two different things, on
 * purpose, kept in separate sections rather than merged into one table.
 */
import { computed } from 'vue';
import { sentence } from '../../composables/useFormat';
import { applyVars, dep, persistVars, row } from '../../composables/useDeployment';

import { conflictingVars } from '../../composables/useVars';
import VarEditor from '../../components/VarEditor.vue';

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
      </p>

      <VarEditor v-model="dep.vars" @change="persistVars" />

      <div class="row" style="margin-top: var(--s3)">
        <button class="primary" @click="applyVars">Apply &amp; reload</button>
        <span class="mute" style="font-size: var(--t-xs)">
          {{ varCount ? `${varCount} variable${varCount === 1 ? '' : 's'}` : 'no variables' }}
        </span>
      </div>

      <p class="hint">
        Add any variable you need — the list below is only what the spec declares.
      </p>
    </section>

    <!--
      Newer servers store variables WITH the deployment, which makes `down` self-describing — the
      strictly better arrangement. It does not make the warning above obsolete: a request variable
      still OVERRIDES a stored one, so a different value typed here retargets the stack.
    -->
    <section v-if="storedList.length" class="panel">
      <h2 class="section" style="margin-bottom: var(--s3)">Stored on the server</h2>
      <p class="dim" style="font-size: var(--t-sm)">Anything set above overrides these.</p>
      <ul class="kvlist" style="margin-top: var(--s3)">
        <li v-for="[k, v] in storedList" :key="k">
          <span class="k"><b>{{ k }}</b></span>
          <span class="v mono">{{ v }}</span>
        </li>
      </ul>

      <div v-if="clashes.length" class="banner warn">
        <b>Overridden: {{ clashes.join(', ') }}.</b>
        <p>This page and the CLI resolve to different stacks. Clear the override unless you mean it.</p>
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
              <th>Key</th>
              <th>Value</th>
              <th>Visibility</th>
              <th>Length</th>
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
                  {{ sentence(e.visibility) }}
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
          A <b>masked</b> value was never sent to this page — there is nothing to reveal.
        </p>
      </template>
    </section>
  </div>
</template>
