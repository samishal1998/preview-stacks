<script setup lang="ts">
/**
 * Submit or replace a deployment.
 *
 * The server's 400 is the whole point of this screen. It carries the SpecError verbatim — the key
 * or variable that is wrong, often the corrected form, and the assert-lint warnings about an
 * `assert_gone` that fails open. That text is the tool's main teaching surface, so it is rendered
 * whole, with newlines intact, and never summarised.
 *
 * The spec is parsed BEFORE anything touches disk, so a rejected submission leaves nothing behind
 * and, on a replace, a good record is never destroyed over a typo.
 */
import { computed, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { api, problem } from '../api/client';
import InfoHint from '../components/InfoHint.vue';
import type {
  DeploymentSourceResponse,
  SpecMeta,
  SpecsResponse,
  SubmitResponse,
  VarPair,
} from '../api/types';
import { loadDeployments, rowFor } from '../composables/useControlPlane';
import { pairsFromRecord, readVars, recordFromPairs, writeVars } from '../composables/useVars';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import VarEditor from '../components/VarEditor.vue';
import SelectMenu from '../components/SelectMenu.vue';

const props = defineProps<{ id?: string }>();

const form = ref({ id: props.id ?? '', spec: '', compose: '' });
const vars = ref<VarPair[]>([{ k: '', v: '' }]);
const source = ref<'inline' | 'stored'>('inline');
const specName = ref('');

const submitting = ref(false);
const error = ref('');
const result = ref<{
  id: string;
  stack: string;
  created: boolean;
  /** Other deployments resolving to the same stack — reported by the server on a new deployment. */
  stackSharedWith?: string[];
} | null>(null);

/**
 * Stored specs, when this server has them. A build without `/api/specs` answers 404, which is not
 * an error here — the picker simply is not offered and inline stays the only shape.
 */
const specs = ref<SpecMeta[]>([]);

/** `— choose —` stays first: an empty value is a real state here, not a placeholder. */
const specOptions = computed(() => [
  { value: '', label: '— choose —' },
  ...specs.value.map((s) => ({ value: s.name, label: s.name, hint: s.kind })),
]);
void api.get<SpecsResponse>('/api/specs').then((r) => {
  if (r.ok && Array.isArray(r.body.specs)) specs.value = r.body.specs;
});

const chosenSpec = computed(() => specs.value.find((s) => s.name === specName.value));

const replacing = computed(() => !!props.id);

/**
 * Duplicate mode: `/submit?from=<id>` copies an existing deployment's spec, compose and variables into
 * a NEW submission. Distinct from replacing — the id starts empty so you name the new one, and nothing
 * is written over.
 *
 * The `stack:` line is the thing to watch, which is why the banner says so: copy a spec whose stack is a
 * literal and both deployments drive the same compose project, so `down` on either stops the other's
 * containers. A spec whose stack interpolates a variable (`pr-${PR}`) just needs a different value. The
 * server independently reports a collision at submit time — see `stackSharedWith`.
 */
const route = useRoute();
const copyFrom = computed(() => {
  const v = route.query.from;
  return typeof v === 'string' && v ? v : null;
});

/**
 * Replacing now PRE-FILLS from what is stored.
 *
 * It used to start empty, because nothing could hand the source back. Replacing is whole-record, so
 * an operator retyping a spec from memory silently dropped whatever they forgot — and a dropped axis
 * stops being tracked while the resources it created keep running. Being unable to read your own
 * submission is what made that a likely mistake rather than a careless one.
 *
 * The body is still cleared FIRST, on every id change. A leftover body from another deployment is the
 * dangerous case: it is perfectly valid YAML, so the PUT would succeed and overwrite the wrong
 * deployment with nothing able to catch it. Clearing first means a failed or withheld fetch leaves the
 * form empty rather than holding someone else's spec.
 */
const sourceState = ref<'idle' | 'loading' | 'loaded' | 'withheld' | 'failed'>('idle');
/** Set when this deployment tracks a stored spec — editing this copy forks it from the original. */
const forkWarning = ref<string | null>(null);

async function loadSource(id: string): Promise<void> {
  sourceState.value = 'loading';
  forkWarning.value = null;
  const r = await api.getAuthed<DeploymentSourceResponse>(
    `/api/deployments/${encodeURIComponent(id)}/source`,
  );
  // A build of the API without the route answers 404; that is a capability difference, not an error,
  // and it must land on the old empty-form behaviour rather than an alarming banner.
  if (r.status === 404 || !r.ok) {
    sourceState.value = 'failed';
    return;
  }
  if (r.body.sourceWithheld) {
    sourceState.value = 'withheld';
    if (r.body.specName) forkWarning.value = r.body.specName;
    return;
  }
  // Only fill if the user has not started typing — a slow fetch must never overwrite their edit.
  if (!form.value.spec) form.value.spec = r.body.spec ?? '';
  if (!form.value.compose) form.value.compose = r.body.compose ?? '';
  if (r.body.specName) forkWarning.value = r.body.specName;
  sourceState.value = 'loaded';
}

// Duplicate: same fetch, but the id stays empty and the variables come across too, since a copied
// spec interpolates the same names.
watch(
  copyFrom,
  async (from) => {
    if (!from || props.id) return;
    error.value = '';
    result.value = null;
    form.value.spec = '';
    form.value.compose = '';
    const saved = rowFor(from)?.vars;
    if (saved) vars.value = pairsFromRecord(saved);
    await loadSource(from);
    // A copied record's own id is not a name for the new one; the operator picks that.
    form.value.id = '';
  },
  { immediate: true },
);

watch(
  () => props.id,
  (id) => {
    error.value = '';
    result.value = null;
    sourceState.value = 'idle';
    forkWarning.value = null;
    if (!id) return;
    if (id !== form.value.id) {
      form.value.spec = '';
      form.value.compose = '';
    }
    form.value.id = id;
    // Seed the variables from what this deployment already uses: replacing a spec usually needs
    // the same ones that resolved it.
    const stored = rowFor(id)?.vars;
    const saved = stored ? pairsFromRecord(stored) : readVars(id);
    if (saved.some((e) => e.k)) vars.value = saved;
    void loadSource(id);
  },
  { immediate: true },
);

const canSubmit = computed(
  () =>
    !!form.value.id &&
    (source.value === 'stored' ? !!specName.value : !!form.value.spec.trim()) &&
    !submitting.value,
);

async function submit(): Promise<void> {
  submitting.value = true;
  error.value = '';
  result.value = null;

  const id = form.value.id;
  const record = recordFromPairs(vars.value);
  /**
   * `vars` are STORED with the deployment on servers that support it, which makes a later `down`
   * resolve the same stack `up` created with no query parameters at all. `env` validates this
   * submission on every server, new or old. Sending both is what keeps this form working against
   * either build — an older server ignores the key it does not know.
   */
  const body: Record<string, unknown> = { vars: record, env: record };
  if (source.value === 'stored') body.specName = specName.value;
  else {
    body.spec = form.value.spec;
    if (form.value.compose.trim()) body.compose = form.value.compose;
  }

  const r = await api.put<SubmitResponse>(`/api/deployments/${encodeURIComponent(id)}`, body);
  submitting.value = false;

  if (r.status === 200 || r.status === 201) {
    result.value = {
      id: r.body.id ?? id,
      stack: r.body.stack ?? '?',
      created: r.status === 201,
      stackSharedWith: r.body.stackSharedWith,
    };
    // Persist the variables that just validated this spec. On an older server this is the single
    // thing keeping a later `down` pointed at the stack the `up` created.
    writeVars(id, vars.value);
    void loadDeployments();
    toast('ok', `${r.status === 201 ? 'Created' : 'Replaced'} ${id} → ${result.value.stack}`, {
      to: `/deployments/${encodeURIComponent(id)}`,
      toLabel: 'Open',
    });
    return;
  }
  // 400 (SpecError), 409 (a job in flight over this stack), 401, 404 — all rendered verbatim.
  error.value = problem(r, 'save this deployment');
}

/**
 * The preview domain, guessed from where this page is served.
 *
 * The UI lives on a host UNDER the preview domain (ui.preview.example.com, pstack.preview…), so
 * previews live beside it: drop this page's own first label and that is the base every generated
 * hostname hangs off. localhost and bare IPs have no domain to offer — those keep an honest
 * placeholder rather than generating `pr-123.192.168.1.5`.
 */
function detectPreviewDomain(): string {
  const h = location.hostname;
  if (h === 'localhost' || h.endsWith('.localhost') || /^[\d.]+$/.test(h) || h.includes(':')) {
    return 'preview.example.com';
  }
  const labels = h.split('.');
  return labels.length >= 3 ? labels.slice(1).join('.') : h;
}

/**
 * Kept as a function of the domain so the example is true on the host it is loaded on — a spec
 * copied from here should deploy without edits, not after find-and-replacing example.com.
 *
 * The compose half uses `pstack.routing.port`, NOT hand-written traefik labels: pstack generates
 * the router, TLS and network wiring from that one label. A bare `traefik.enable=true` is the
 * OPT-OUT signal (any traefik.* label means "leave my service alone") — which is what an older
 * version of this example taught, producing a service that was never reachable.
 */
const exampleSpec = (domain: string): string => `version: 1
kind: isolated

# The stack identity: the compose project name, and $STACK inside every hook.
# \${PR} is interpolated ONCE, at resolve time, from the variables sent with the request.
stack: pr-\${PR}

env:
  # The base every generated hostname hangs off: the app below is served at
  # app-pr-\${PR}.${domain}
  PREVIEW_DOMAIN: ${domain}

compose:
  file: compose.yml
  # Every service sits behind a profile so a bare \`up\` starts nothing.
  # \`down\` always enables ALL of them — otherwise the unselected profiles' network leaks.
  profiles: [app]

requires:
  - name: traefik-network
    assert: docker network inspect traefik >/dev/null 2>&1
    hint: run \`pstack init\` on the host first

axes:
  # Hooks run from the deployment directory, which holds only spec.yml and compose.yml —
  # so use inline shell or absolute paths, never ./hooks/foo.sh.
  - name: scratch
    up: mkdir -p /tmp/$STACK
    assert_live: test -d /tmp/$STACK
    down: rm -rf /tmp/$STACK
    # assert_gone MUST FAIL CLOSED. A bare \`! <probe>\` reports "gone" whenever the probe fails
    # for any reason — a missing CLI, an expired token — turning "I could not tell" into
    # "it is gone", a false negative on the one thing this tool exists to catch.
    assert_gone: |
      test -d /tmp || exit 1
      ! test -d /tmp/$STACK
`;

const EXAMPLE_COMPOSE = `services:
  app:
    image: nginx:alpine
    profiles: [app]
    labels:
      # The ONE label routing needs: pstack generates the Traefik router, TLS and network wiring
      # from it. Writing any traefik.* label yourself is the opt-out — pstack then leaves the
      # service entirely alone, so only do that when you need something the shorthand cannot say.
      - pstack.routing.port=80
`;

function loadExample(): void {
  source.value = 'inline';
  form.value.spec = exampleSpec(detectPreviewDomain());
  if (!form.value.compose.trim()) form.value.compose = EXAMPLE_COMPOSE;
  if (!form.value.id) form.value.id = 'pr-123';
  if (!vars.value.some((e) => e.k)) vars.value = [{ k: 'PR', v: '123' }];
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>
          {{ replacing ? 'Edit a deployment' : copyFrom ? 'Duplicate a deployment' : 'Submit a deployment' }}
        </h1>
        <div class="sub">
          {{
            replacing
              ? 'Loaded from what is stored; saving replaces the record'
              : copyFrom
                ? `Copied from ${copyFrom} — give it a new id, then edit`
                : 'Stores a spec so it can be deployed'
          }}
        </div>
      </div>
      <span class="grow" />
      <button class="ghost" @click="loadExample">Load example spec</button>
    </div>

    <section class="panel">
      <!-- The "nothing saved until valid" reassurance lives beside the Submit button below — the
           moment of commitment is where a guarantee about committing belongs, not floating above
           an unrelated id field. -->

      <!--
        A `PUT` REPLACES the whole record, and this form cannot pre-fill it: the API has no route
        that returns a stored spec. Say that out loud, because a half-remembered spec pasted here
        is still valid YAML — the server will accept it, and the axes the original declared simply
        stop existing as far as the control plane knows, while whatever they created keeps running.
      -->
      <!--
        The one thing that makes duplicating dangerous, said before the form rather than after the
        submission: two records resolving to one stack drive the same compose project.
      -->
      <div v-if="copyFrom && !replacing" class="banner warn">
        <b>Change the stack, not just the id.</b>
        <p>
          Two deployments that resolve to the same <code>stack</code> drive the same compose project —
          <code>down</code> on either stops the other's containers. If the stack interpolates a variable
          (<code>pr-${PR}</code>) give it a different value below; if it is a literal, edit the
          <code>stack:</code> line.
          <template v-if="sourceState === 'withheld'">
            The spec could not be copied without an access token —
            <RouterLink to="/settings">add yours</RouterLink>.
          </template>
        </p>
      </div>

      <div v-if="replacing" class="banner" :class="sourceState === 'loaded' ? 'info' : 'warn'">
        <b>Replacing {{ id }}.</b>
        <p v-if="sourceState === 'loading'">Loading what is stored…</p>
        <p v-else-if="sourceState === 'loaded'">
          Loaded from the stored record — edit it and save. Saving replaces the record outright, so
          anything you delete here stops being tracked while whatever it created keeps running.
        </p>
        <p v-else-if="sourceState === 'withheld'">
          The stored spec was not loaded: reading it needs an access token, because hooks routinely
          carry a credential inline. <RouterLink to="/settings">Add your token</RouterLink> to edit
          in place — otherwise paste the <b>complete</b> spec, since anything you leave out stops
          being tracked.
        </p>
        <p v-else>
          The stored spec could not be loaded, so this form is empty. Paste the <b>complete</b> spec
          you want stored — anything you leave out stops being tracked while whatever it created keeps
          running.
        </p>
      </div>

      <!--
        A deployment that references a stored spec keeps its own copy of the source. Editing that copy
        forks it: the stored spec every other deployment shares is untouched, and this one stops
        following it. Silent forks are the kind of thing discovered months later.
      -->
      <div v-if="forkWarning" class="banner warn">
        <b>This deployment tracks the stored spec “{{ forkWarning }}”.</b>
        <p>
          Saving here writes a private copy and stops it following that spec. To change every
          deployment using it, edit
          <RouterLink :to="`/specs/${encodeURIComponent(forkWarning)}`">the spec</RouterLink> instead.
        </p>
      </div>

      <div class="row" style="margin-bottom: var(--s4)">
        <div class="field" style="max-width: 280px">
          <label for="did">Deployment id</label>
          <input
            id="did"
            v-model.trim="form.id"
            type="text"
            placeholder="pr-123"
            spellcheck="false"
            autocomplete="off"
          />
        </div>
        <span class="mute" style="align-self: end; padding-bottom: 10px">
          Lower case
          <InfoHint label="allowed characters in a deployment id">
            Starts with a letter or digit, then letters, digits, dot, dash or underscore. Up to 64
            characters — <code>[a-z0-9][a-z0-9._-]{0,63}</code>.
          </InfoHint>
        </span>
      </div>

      <!-- Offered only when this server actually has stored specs. -->
      <div v-if="specs.length" class="row" style="margin-bottom: var(--s4)">
        <label class="check">
          <input v-model="source" type="radio" value="inline" /> inline spec
        </label>
        <label class="check">
          <input v-model="source" type="radio" value="stored" /> reference a stored spec
        </label>
      </div>

      <template v-if="source === 'stored' && specs.length">
        <div class="field" style="max-width: 340px">
          <label for="sn">Stored spec</label>
          <SelectMenu v-model="specName" id="sn" label="Stored spec" :options="specOptions" />
        </div>
        <p v-if="chosenSpec" class="hint">
          <span v-if="chosenSpec.description">{{ chosenSpec.description }} — </span>
          needs
          <b v-if="chosenSpec.requiredVars.length">{{ chosenSpec.requiredVars.join(', ') }}</b>
          <span v-else>no variables</span>.
          <InfoHint label="why variables are required">
            A missing variable is refused by name. It is never filled in with a blank, which would
            silently give every deployment the same stack name.
          </InfoHint>
        </p>
      </template>

      <template v-else>
        <div class="field">
          <label for="spec">Spec.yml</label>
          <textarea
            id="spec"
            v-model="form.spec"
            rows="12"
            spellcheck="false"
            placeholder="version: 1&#10;kind: isolated&#10;stack: pr-${PR}&#10;axes: []"
          />
        </div>

        <div class="field" style="margin-top: var(--s3)">
          <label for="compose">compose.yml <span class="mute">(optional)</span></label>
          <textarea
            id="compose"
            v-model="form.compose"
            rows="6"
            spellcheck="false"
            placeholder="Written next to spec.yml, so a compose file named compose.yml resolves"
          />
        </div>
        <p class="hint">
          Hooks must be inline shell or an absolute path.
          <InfoHint label="why a relative script path does not work">
            A deployment directory only ever holds <code>spec.yml</code> and
            <code>compose.yml</code>, and hooks run from there — so
            <code>up: ./hooks/db.sh</code> has nothing to find.
          </InfoHint>
        </p>
      </template>

      <h2 class="section" style="margin: var(--s5) 0 var(--s3)">Variables</h2>
      <VarEditor v-model="vars" />
      <p class="hint">
        Saved with the deployment, so tearing it down later targets the same stack.
        <InfoHint label="how variables are remembered">
          They are stored on the server where it supports that, and in this browser either way — so
          the actions on the deployment page send exactly these values.
        </InfoHint>
      </p>

      <div class="row" style="margin-top: var(--s5)">
        <ActionButton variant="primary" :pending="submitting" :disabled="!canSubmit" @click="submit">
          {{ submitting ? 'Submitting…' : 'Submit' }}
        </ActionButton>
        <span v-if="!result" class="mute" style="font-size: var(--t-sm)">
          Nothing is saved until the spec is valid
          <InfoHint label="what happens on a rejected submission">
            The spec is checked before anything is written, so a rejected submission leaves nothing
            behind — and a replace never destroys a good record over a typo. Whether the deployment
            is shared or isolated comes from the spec, not from this form.
          </InfoHint>
        </span>
        <span v-if="result" class="s-ok">
          {{ result.created ? 'Created' : 'Replaced' }} — stack
          <b>{{ result.stack }}</b>
          <RouterLink :to="`/deployments/${encodeURIComponent(result.id)}`" style="margin-left: 8px">
            open →
          </RouterLink>
        </span>
      </div>

      <!--
        The server's rejection text IS the documentation: it names the key, the variable or the
        assert that is wrong, and often prints the corrected form. Rendered whole, newlines and
        indentation intact. Never truncate it, never collapse it, never say "invalid spec".
      -->
      <!--
        Reported by the server, which resolved every other deployment to check. Shown after the fact
        because it can only be known once the spec has resolved — and it is a warning, not a rejection:
        it can be deliberate, and refusing over a guess would be worse than saying so.
      -->
      <div v-if="result?.stackSharedWith?.length" class="banner warn">
        <b>Another deployment already resolves to stack “{{ result.stack }}”.</b>
        <p>
          <template v-for="(o, i) in result.stackSharedWith" :key="o">
            <RouterLink :to="`/deployments/${encodeURIComponent(o)}`">{{ o }}</RouterLink
            ><span v-if="i < result.stackSharedWith.length - 1">, </span> </template
          >— both records now drive the same compose project, so <code>down</code> on either stops the
          other's containers and <code>verify</code> on either reports the other's leaks. If that was not
          intended, change this one's <code>stack</code> and save again.
        </p>
      </div>

      <ErrorNote v-if="error" :text="error" title="The server rejected this submission." />
    </section>
  </div>
</template>
