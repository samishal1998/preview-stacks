<script setup lang="ts">
/**
 * Submit, replace or duplicate a deployment.
 *
 * The server's 400 is the point of this screen: it carries the SpecError verbatim (the key or
 * variable that is wrong, often the corrected form, and assert lints). It is shown whole, newlines
 * intact, never summarised. The spec is parsed before anything touches disk, so a rejected
 * submission leaves nothing behind and a good record is never destroyed over a typo.
 */
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import {
  HAlert,
  HButton,
  HCheckbox,
  HFileUpload,
  HInput,
  HPageHeader,
  HSegmentedControl,
  HSelect,
  HStack,
  HText,
} from '@hearth-ui/vue';
import { deploymentApi } from '../api-deployment';
import { deploymentSource, listSpecs, submit as putDeployment, type SpecMeta, type SubmitResponse } from '../api-specs';
import { blocker, EXAMPLE_COMPOSE, exampleSpec, previewDomain, routeFiles, rowVars, submitBody, validName } from '../lib/specs';
import { problem } from '../lib/problem';
import { pairsFromRecord, type VarPair } from '../lib/vars';
import { can } from '../lib/users';
import { auth, data, refresh, toast } from '../state';
import { readVars, writeVars } from '../vars/store';
import VarEditor from '../vars/VarEditor.vue';
import MonoTextarea from '../components/MonoTextarea.vue';
import VerbatimAlert from '../components/VerbatimAlert.vue';
import { detail } from '../vars/useDetail';

const props = defineProps<{ id?: string }>();
const route = useRoute();
const router = useRouter();

const depId = ref(props.id ?? '');
const spec = ref('');
const compose = ref('');
const vars = ref<VarPair[]>([{ k: '', v: '' }]);
const source = ref<'inline' | 'stored'>('inline');
const specName = ref('');
const deploy = ref(false);

const busy = ref(false);
const error = ref('');
const deployError = ref('');
const result = ref<(SubmitResponse & { created: boolean }) | null>(null);

const replacing = computed(() => !!props.id);
const q = (k: 'from' | 'spec'): string | null => {
  const v = route.query[k];
  return typeof v === 'string' && v ? v : null;
};
const copyFrom = computed(() => (replacing.value ? null : q('from')));

// ── stored specs; a 404 means the picker is simply not offered ──────────────────────────────────
const specs = ref<SpecMeta[]>([]);
onMounted(async () => {
  const r = await listSpecs();
  if (r.ok && Array.isArray(r.body.specs)) specs.value = r.body.specs;
  const want = q('spec');
  if (want && specs.value.some((s) => s.name === want) && !props.id) {
    source.value = 'stored';
    specName.value = want;
  }
});
const chosen = computed(() => specs.value.find((s) => s.name === specName.value));
const specOptions = computed(() => specs.value.map((s) => ({ value: s.name, label: `${s.name} (${s.kind})` })));
const modeOptions = [
  { value: 'inline', label: 'Inline spec' },
  { value: 'stored', label: 'Stored spec' },
];

// ── prefill from what is stored ─────────────────────────────────────────────────────────────────
// The body is cleared FIRST on every id change: a leftover body from another deployment is valid
// YAML, so the PUT would succeed and overwrite the wrong record. A failed or withheld fetch must
// leave the form empty, never holding someone else's spec.
const sourceState = ref<'idle' | 'loading' | 'loaded' | 'withheld' | 'failed'>('idle');
const forkOf = ref<string | null>(null);

let seq = 0;
async function loadSource(dep: string): Promise<void> {
  const mine = ++seq;
  sourceState.value = 'loading';
  forkOf.value = null;
  const r = await deploymentSource(dep);
  if (mine !== seq) return; // another record was opened meanwhile
  // 404 is a build without the route: a capability difference, not an error.
  if (!r.ok) {
    sourceState.value = 'failed';
    return;
  }
  if (r.body.specName) forkOf.value = r.body.specName;
  if (r.body.sourceWithheld) {
    sourceState.value = 'withheld';
    return;
  }
  // A slow fetch must never overwrite what the person already typed.
  if (!spec.value) spec.value = r.body.spec ?? '';
  if (!compose.value) compose.value = r.body.compose ?? '';
  sourceState.value = 'loaded';
}

function reset(): void {
  seq++;
  error.value = '';
  deployError.value = '';
  result.value = null;
  sourceState.value = 'idle';
  forkOf.value = null;
}

watch(
  () => [props.id, copyFrom.value] as const,
  async ([id, from]) => {
    reset();
    if (id) {
      if (id !== depId.value) {
        spec.value = '';
        compose.value = '';
        vars.value = [{ k: '', v: '' }];
      }
      depId.value = id;
      // Replacing usually needs the variables that resolved the stored record.
      const stored = rowVars(data.deployments.find((d) => d.id === id));
      const saved = stored ? pairsFromRecord(stored) : readVars(id);
      if (saved.some((e) => e.k)) vars.value = saved;
      await loadSource(id);
    } else if (from) {
      // Duplicate: same fetch, empty id, variables come across.
      spec.value = '';
      compose.value = '';
      const stored = rowVars(data.deployments.find((d) => d.id === from));
      if (stored) vars.value = pairsFromRecord(stored);
      await loadSource(from);
      depId.value = '';
    } else {
      // Plain submit: nothing carries over from a record opened before.
      depId.value = '';
      spec.value = '';
      compose.value = '';
      specName.value = '';
      deploy.value = false;
      vars.value = [{ k: '', v: '' }];
    }
  },
  { immediate: true },
);

const form = computed(() => ({ source: source.value, spec: spec.value, compose: compose.value, specName: specName.value }));
const why = computed(() => (can('developer', auth) ? blocker(depId.value, form.value) : 'Needs the developer role.'));
const idError = computed(() => (depId.value && !validName(depId.value) ? 'Lower case letters, digits, . _ - (max 64).' : undefined));

async function submit(): Promise<void> {
  busy.value = true;
  error.value = '';
  deployError.value = '';
  result.value = null;
  const dep = depId.value;
  const r = await putDeployment(dep, submitBody(form.value, vars.value));
  if (r.status !== 200 && r.status !== 201) {
    busy.value = false;
    // 400 SpecError, 409 a job in flight over this stack, 401, 403: all shown verbatim.
    error.value = problem(r, 'Save this deployment');
    return;
  }
  result.value = { ...r.body, id: r.body.id ?? dep, created: r.status === 201 };
  // Older servers do not store vars; this is what keeps a later teardown on the right stack.
  writeVars(dep, vars.value);
  detail.id = ''; // the Config, Axes and Requires tabs resolve again
  void refresh();
  toast(`${result.value.created ? 'Created' : 'Replaced'} ${dep}`, 'success', `Stack ${r.body.stack}`);
  if (deploy.value) {
    const a = await deploymentApi.act(dep, 'up');
    busy.value = false;
    if (a.status === 202 && a.body.job) {
      void router.push(`/jobs/${encodeURIComponent(a.body.job.id)}`);
      return;
    }
    deployError.value = problem(a, 'Deploy');
    return;
  }
  busy.value = false;
}

function loadExample(): void {
  source.value = 'inline';
  spec.value = exampleSpec(previewDomain(location.hostname));
  if (!compose.value.trim()) compose.value = EXAMPLE_COMPOSE;
  if (!depId.value) depId.value = 'pr-123';
  if (!vars.value.some((e) => e.k)) vars.value = [{ k: 'PR', v: '123' }];
}

// ── load from files ─────────────────────────────────────────────────────────────────────────────
const files = ref<File[]>([]);
const fileKey = ref(0);
const fileError = ref('');
async function onFiles(list: File[]): Promise<void> {
  if (!list.length) return;
  try {
    const got = routeFiles(await Promise.all(list.map(async (f) => ({ name: f.name, text: await f.text() }))));
    if (got.spec !== undefined) spec.value = got.spec;
    if (got.compose !== undefined) compose.value = got.compose;
    source.value = 'inline';
    fileError.value = '';
  } catch {
    fileError.value = 'Could not read the file.';
  }
  files.value = [];
  fileKey.value++;
}

const title = computed(() => (replacing.value ? `Edit ${props.id}` : copyFrom.value ? 'Duplicate a deployment' : 'Submit a deployment'));
const description = computed(() =>
  replacing.value ? 'Saving replaces the record.' : copyFrom.value ? `Copied from ${copyFrom.value}. Give it a new id.` : 'Store a spec, then deploy it.',
);
</script>

<template>
  <HStack direction="column" :gap="4">
    <HPageHeader :title="title" :description="description" actions-placement="end">
      <template #actions>
        <HButton variant="secondary" icon="code" label="Load example" @click="loadExample" />
      </template>
    </HPageHeader>

    <HAlert v-if="copyFrom" tone="warning" title="Change the stack, not just the id">
      Two deployments on one stack share a compose project: tearing down either stops both.
      <RouterLink v-if="sourceState === 'withheld'" to="/settings">Sign in to copy the spec.</RouterLink>
    </HAlert>

    <HAlert v-if="replacing && sourceState === 'loading'" tone="info" compact description="Loading the stored spec." />
    <HAlert v-else-if="replacing" :tone="sourceState === 'loaded' ? 'info' : 'warning'" :title="`Replacing ${id}`">
      <template v-if="sourceState === 'loaded'">Anything you delete stops being tracked but keeps running.</template>
      <template v-else-if="sourceState === 'withheld'">Stored spec hidden. Paste the complete spec: anything you leave out stops being tracked but keeps running.</template>
      <template v-else>Stored spec unavailable. Paste the complete spec.</template>
    </HAlert>

    <HAlert v-if="forkOf" tone="warning" :title="`Tracks the stored spec ${forkOf}`">
      Saving forks it.
      <RouterLink :to="`/specs/${encodeURIComponent(forkOf)}`">Edit the spec</RouterLink> to change every deployment using it.
    </HAlert>

    <HInput
      v-model="depId"
      label="Deployment id"
      placeholder="pr-123"
      hint="Lower case letters, digits, . _ -"
      autocomplete="off"
      spellcheck="false"
      :readonly="replacing"
      :error="idError"
    />

    <HSegmentedControl v-if="specs.length" v-model="source" label="Spec source" :options="modeOptions" />

    <template v-if="source === 'stored' && specs.length">
      <HSelect v-model="specName" label="Stored spec" placeholder="Choose a spec" :options="specOptions" />
      <HText v-if="chosen" tone="muted">
        {{ chosen.description ? `${chosen.description}. ` : '' }}Needs {{ chosen.requiredVars.length ? chosen.requiredVars.join(', ') : 'no variables' }}.
      </HText>
    </template>

    <template v-else>
      <HFileUpload
        v-model="files"
        :reset-key="fileKey"
        label="Load from files"
        description="spec.yml, and compose.yml if the name has “compose”."
        accept=".yml,.yaml,text/yaml"
        multiple
        :max-files="2"
        :max-size="1048576"
        :error="fileError || undefined"
        @change="onFiles"
        @reject="fileError = $event.map((x) => `${x.name}: ${x.reason}`).join('. ')"
      />
      <MonoTextarea
        v-model="spec"
        label="spec.yml"
        :rows="14"
        resize="vertical"
        spellcheck="false"
        placeholder="version: 1&#10;kind: isolated&#10;stack: pr-${PR}&#10;axes: []"
      />
      <MonoTextarea
        v-model="compose"
        label="compose.yml"
        hint="Optional. Written next to spec.yml. Hooks must be inline shell or an absolute path."
        :rows="7"
        resize="vertical"
        spellcheck="false"
      />
    </template>

    <HStack direction="column" :gap="2">
      <HText weight="bold">Variables</HText>
      <VarEditor v-model="vars" :disabled="busy" />
      <HText tone="muted">Saved with the deployment, so teardown targets the same stack.</HText>
    </HStack>

    <HCheckbox v-model="deploy" label="Deploy after saving" />

    <HStack direction="row" :gap="3" align="center" wrap>
      <HButton variant="primary" :label="deploy ? 'Save and deploy' : 'Save'" :loading="busy" :disabled="!!why" @click="submit" />
      <HText tone="muted">{{ why || 'Nothing is saved until the spec is valid.' }}</HText>
    </HStack>

    <HAlert v-if="result" tone="success" :title="`${result.created ? 'Created' : 'Replaced'} ${result.id}`">
      Stack {{ result.stack }}.
      <RouterLink :to="`/deployments/${encodeURIComponent(result.id)}`">Open</RouterLink>
    </HAlert>
    <HAlert v-if="result?.stackSharedWith?.length" tone="warning" :title="`Stack ${result.stack} is already in use`">
      <template v-for="(o, i) in result.stackSharedWith" :key="o">
        <RouterLink :to="`/deployments/${encodeURIComponent(o)}`">{{ o }}</RouterLink><span v-if="i < result.stackSharedWith.length - 1">, </span>
      </template>
      share it. Tearing down either stops both. Change this stack if that is not intended.
    </HAlert>
    <HAlert v-if="result?.swarmNotes?.length" tone="warning" title="Swarm changes this compose file">
      <ul class="notes">
        <li v-for="n in result.swarmNotes" :key="n">{{ n }}</li>
      </ul>
    </HAlert>
    <VerbatimAlert v-if="error" title="Not saved" :text="error" />
    <VerbatimAlert v-if="deployError" title="Saved, deploy failed" :text="deployError" />
  </HStack>
</template>

<style scoped>
.notes {
  margin: 0;
  padding-left: 1.2em;
}
</style>
