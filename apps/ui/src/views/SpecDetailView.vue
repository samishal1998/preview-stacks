<script setup lang="ts">
/**
 * One stored spec: what it needs, who uses it, and its source.
 *
 * THE SOURCE IS A RESTRICTED READ. Everything else on this API can be read without a token, but a
 * spec's hook bodies are shell strings that routinely carry a credential inline, so the server sends
 * `source` only to an authenticated request and sets `sourceWithheld` otherwise. That is why this
 * one view uses `getAuthed`, and why "no token" renders an explanation rather than an empty editor —
 * a blank box would read as an empty spec, which is a different and much more alarming fact.
 */
import { computed, ref, watch } from 'vue';
import { api, problem } from '../api/client';
import type { SpecDetail } from '../api/types';
import { state, loadDeployments } from '../composables/useControlPlane';
import { settings } from '../composables/useSettings';
import { stamp } from '../composables/useFormat';
import SkeletonList from '../components/SkeletonList.vue';
import ErrorNote from '../components/ErrorNote.vue';
import InfoHint from '../components/InfoHint.vue';

const props = defineProps<{ name: string }>();

const spec = ref<SpecDetail | null>(null);
const loading = ref(true);
const error = ref('');
const missing = ref(false);

async function load(): Promise<void> {
  loading.value = true;
  missing.value = false;
  const r = await api.getAuthed<SpecDetail>(`/api/specs/${encodeURIComponent(props.name)}`);
  loading.value = false;
  if (r.status === 404) {
    missing.value = true;
    return;
  }
  if (!r.ok) {
    error.value = problem(r, 'load this spec');
    return;
  }
  error.value = '';
  spec.value = r.body;
}

watch(() => props.name, load, { immediate: true });
// Re-fetch when a token is pasted: the same URL returns strictly more with one.
watch(() => settings.token, load);

if (!state.deploymentsLoaded) void loadDeployments();

/** Deployments that would become unresolvable if this spec were deleted. */
const users = computed(() => state.deployments.filter((d) => d.specName === props.name));
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="mute" style="font-size: var(--t-sm)">
          <RouterLink to="/specs">← specs</RouterLink>
        </div>
        <h1 style="font-size: var(--t-xl)">{{ name }}</h1>
        <div class="sub">
          <span v-if="spec?.description">{{ spec.description }}</span>
          <span v-else-if="missing">not found</span>
          <span v-else-if="loading">loading…</span>
          <span v-else class="mute">no description</span>
        </div>
      </div>
      <span class="grow" />
      <span v-if="spec" class="badge" :class="spec.kind">{{ spec.kind }}</span>
    </div>

    <ErrorNote v-if="error" :text="error" title="Could not load this spec." />

    <section v-if="missing" class="panel">
      <div class="banner failed">
        <b>No spec named “{{ name }}”.</b>
        <p>It may have been deleted, or this is an older link.</p>
      </div>
    </section>

    <template v-else>
      <section class="panel">
        <SkeletonList v-if="loading" :rows="4" />
        <ul v-else-if="spec" class="kvlist">
          <li>
            <span class="k">
              needs
              <InfoHint label="what needs means">
                Variables the spec uses but does not set. Every deployment referencing it supplies
                these, which is how one spec serves many stacks — and a missing one is refused by
                name rather than filled in blank.
              </InfoHint>
            </span>
            <span class="v">
              <b v-if="spec.requiredVars.length">{{ spec.requiredVars.join(', ') }}</b>
              <span v-else class="mute">nothing — every value is fixed by the spec</span>
            </span>
          </li>
          <li>
            <span class="k">used by</span>
            <span class="v">
              <template v-if="users.length">
                <RouterLink
                  v-for="d in users"
                  :key="d.id"
                  :to="`/deployments/${encodeURIComponent(d.id)}`"
                  style="margin-right: 10px"
                  >{{ d.id }}</RouterLink
                >
                <InfoHint label="why this matters">
                  Deleting a spec while a deployment still references it is refused: that deployment
                  could no longer be resolved, and one that cannot be resolved can never be torn
                  down.
                </InfoHint>
              </template>
              <span v-else class="mute">no deployments reference it</span>
            </span>
          </li>
          <li><span class="k">created</span><span class="v">{{ stamp(spec.createdAt) }}</span></li>
          <li><span class="k">updated</span><span class="v">{{ stamp(spec.updatedAt) }}</span></li>
        </ul>
      </section>

      <section v-if="spec" class="panel">
        <div class="phead">
          <h2 class="section">Source</h2>
        </div>

        <!--
          Withheld, not empty. The server never sent the source, so there is nothing here to reveal —
          the same reasoning as the masked variables: an affordance that promises plaintext the page
          does not have would be a lie.
        -->
        <div v-if="spec.sourceWithheld" class="banner plain">
          <b>Hidden without an access token.</b>
          <p>
            A spec's hooks are shell commands, and they often carry a password or an API token
            inline — so the server does not send the file to an unauthenticated reader.
            <RouterLink to="/settings">Add your token</RouterLink> to see it.
          </p>
        </div>
        <pre v-else-if="spec.source" class="code">{{ spec.source }}</pre>
        <p v-else class="mute">The server returned no source for this spec.</p>
      </section>
    </template>
  </div>
</template>
