<script setup lang="ts">
/**
 * Host variables & secrets — the GitHub model, scoped to this host.
 *
 * TWO SECTIONS BECAUSE THEY ARE TWO CONTRACTS. A variable's value is configuration anyone may read
 * back; a secret's value goes IN through this form and never comes back OUT — the list shows its
 * name and when it changed, nothing else, because the server keeps no route that returns it. The
 * form says so at the moment of storing, not in documentation nobody reads.
 *
 * Specs reference these as `${vars.NAME}` and `${secrets.NAME}` — spelled differently from plain
 * `${NAME}` on purpose, so a spec that reads host state says so on its face.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import EquivalentCommand from '../components/EquivalentCommand.vue';
import ErrorNote from '../components/ErrorNote.vue';
import InfoHint from '../components/InfoHint.vue';
import RelativeTime from '../components/RelativeTime.vue';
import SkeletonList from '../components/SkeletonList.vue';

type Entry = { name: string; value: string | null; secret: boolean; updatedAt: number };

const entries = ref<Entry[]>([]);
const loaded = ref(false);
const listError = ref('');
const unsupported = ref(false);

const vars = computed(() => entries.value.filter((e) => !e.secret));
const secrets = computed(() => entries.value.filter((e) => e.secret));

const form = ref({ name: '', value: '', secret: false });
const saving = ref(false);
/** Editing state: the row being replaced. For a secret this means re-entering the value. */
const editing = ref<Entry | null>(null);

const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
const canSave = computed(
  () => !saving.value && NAME.test(form.value.name) && form.value.value.length > 0,
);

async function load(): Promise<void> {
  const r = await api.get<{ entries: Entry[] }>('/api/host-vars');
  loaded.value = true;
  if (r.status === 404) {
    unsupported.value = true;
    return;
  }
  if (!r.ok) {
    listError.value = problem(r, 'load the variables');
    return;
  }
  listError.value = '';
  entries.value = r.body.entries ?? [];
}
void load();

function startEdit(e: Entry): void {
  editing.value = e;
  form.value = { name: e.name, value: e.value ?? '', secret: e.secret };
}

function cancelEdit(): void {
  editing.value = null;
  form.value = { name: '', value: '', secret: false };
}

async function save(): Promise<void> {
  saving.value = true;
  const r = await api.put(`/api/host-vars/${form.value.name}`, {
    value: form.value.value,
    secret: form.value.secret,
  });
  saving.value = false;
  if (!r.ok) {
    listError.value = problem(r, `store ${form.value.name}`);
    return;
  }
  listError.value = '';
  toast('ok', `Stored ${form.value.name}.`);
  cancelEdit();
  void load();
}

async function remove(e: Entry): Promise<void> {
  const r = await api.del(`/api/host-vars/${e.name}`);
  if (!r.ok) {
    listError.value = problem(r, `delete ${e.name}`);
    return;
  }
  toast('ok', `Deleted ${e.name}. Specs referencing it will fail to resolve until it exists again.`);
  if (editing.value?.name === e.name) cancelEdit();
  void load();
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Variables &amp; secrets</h1>
        <div class="sub">
          Host-level values every spec can reference
          <InfoHint label="how a spec uses these">
            Write <code>${vars.NAME}</code> or <code>${secrets.NAME}</code> anywhere a spec
            interpolates — the <code>env:</code> block, hooks, compose settings. The spelling is
            deliberate: plain <code>${NAME}</code> keeps meaning "whatever the request supplied", so
            the two can never collide.
          </InfoHint>
        </div>
      </div>
    </div>

    <ErrorNote v-if="listError" :text="listError" title="Something went wrong." />

    <section v-if="unsupported" class="panel">
      <div class="banner plain">
        <b>This server has no variable store.</b>
        <p>It is an older build of pstack. Upgrade the host to use host-level variables.</p>
      </div>
    </section>

    <template v-else>
      <div class="grid-2">
        <section class="panel">
          <div class="phead">
            <h2 class="section">Variables</h2>
            <InfoHint label="what belongs here">
              Configuration that is fine to read back: a region, an image tag, the log level a
              preview should run at. Referenced as <code>${vars.NAME}</code>.
            </InfoHint>
            <span class="grow" />
            <span class="mute" style="font-size: var(--t-xs)">{{ vars.length }}</span>
          </div>

          <SkeletonList v-if="!loaded" :rows="2" />
          <table v-else-if="vars.length" class="cards">
            <thead>
              <tr>
                <th>Name</th>
                <th>Value</th>
                <th>Updated</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in vars" :key="e.name">
                <td class="name" data-label="name"><code>{{ e.name }}</code></td>
                <td class="break" data-label="value">{{ e.value }}</td>
                <td class="dim nowrap" data-label="updated"><RelativeTime :at="e.updatedAt" /></td>
                <td class="right nowrap" data-label="">
                  <button class="ghost sm" @click="startEdit(e)">Edit</button>
                  <ActionButton class="danger sm" :confirm="`Delete ${e.name}?`" @run="remove(e)">
                    Delete
                  </ActionButton>
                </td>
              </tr>
            </tbody>
          </table>
          <p v-else class="hint">No variables yet.</p>
        </section>

        <section class="panel">
          <div class="phead">
            <h2 class="section">Secrets</h2>
            <InfoHint label="what makes a secret different">
              The value is write-only: no page and no API route ever returns it, and it is scrubbed
              from job and container logs by content. Referenced as <code>${secrets.NAME}</code> —
              refused in <code>stack:</code>, because a stack name appears in container names and
              hostnames that cannot be redacted.
            </InfoHint>
            <span class="grow" />
            <span class="mute" style="font-size: var(--t-xs)">{{ secrets.length }}</span>
          </div>

          <SkeletonList v-if="!loaded" :rows="2" />
          <table v-else-if="secrets.length" class="cards">
            <thead>
              <tr>
                <th>Name</th>
                <th>Updated</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in secrets" :key="e.name">
                <td class="name" data-label="name"><code>{{ e.name }}</code></td>
                <td class="dim nowrap" data-label="updated"><RelativeTime :at="e.updatedAt" /></td>
                <td class="right nowrap" data-label="">
                  <button class="ghost sm" @click="startEdit(e)">Replace</button>
                  <ActionButton class="danger sm" :confirm="`Delete ${e.name}?`" @run="remove(e)">
                    Delete
                  </ActionButton>
                </td>
              </tr>
            </tbody>
          </table>
          <p v-else class="hint">No secrets yet.</p>
        </section>
      </div>

      <section class="panel">
        <h2 class="section" style="margin-bottom: var(--s3)">
          {{ editing ? (editing.secret ? `Replace the value of ${editing.name}` : `Edit ${editing.name}`) : 'Add a variable or secret' }}
        </h2>

        <form @submit.prevent="save">
          <div class="row wrap two-up">
            <label class="field grow">
              <span>Name</span>
              <input
                v-model.trim="form.name"
                type="text"
                placeholder="DB_PASSWORD"
                spellcheck="false"
                autocomplete="off"
                :disabled="!!editing"
              />
            </label>
            <label class="field grow">
              <span>Value</span>
              <input
                v-model="form.value"
                :type="form.secret ? 'password' : 'text'"
                :placeholder="editing?.secret ? 'enter the new value' : 'eu-central'"
                spellcheck="false"
                autocomplete="off"
              />
            </label>
          </div>
          <p v-if="form.name && !NAME.test(form.name)" class="s-failed" style="margin-top: var(--s2)">
            Letters, digits and _ only, not starting with a digit — it has to be referenceable in a spec.
          </p>

          <div class="row" style="margin-top: var(--s3)">
            <label class="check">
              <input v-model="form.secret" type="checkbox" :disabled="editing?.secret === true" />
              Secret — the value can never be read back
            </label>
          </div>
          <p v-if="editing?.secret" class="hint">
            A secret cannot be turned back into a readable variable — that would reveal a value
            stored write-only. Delete it and create a variable instead, re-entering the value.
          </p>

          <div class="row" style="margin-top: var(--s4)">
            <button class="primary" type="submit" :disabled="!canSave">
              {{ editing ? 'Save' : 'Add' }}
            </button>
            <button v-if="editing" type="button" class="ghost" @click="cancelEdit">Cancel</button>
            <span v-if="!editing" class="mute" style="font-size: var(--t-sm)">
              Reference it as <code>${{ '{' }}{{ form.secret ? 'secrets' : 'vars' }}.{{ form.name || 'NAME' }}{{ '}' }}</code> in any spec
            </span>
          </div>
          <EquivalentCommand
            what="storing this value"
            method="PUT"
            :path="`/api/host-vars/${form.name || 'NAME'}`"
            :body="{ value: form.secret ? '…' : form.value || '…', secret: form.secret }"
          />
        </form>
      </section>
    </template>
  </div>
</template>

<style scoped>
td.right {
  text-align: end;
}
td.right button {
  margin-inline-start: var(--s2);
}
.two-up > .field {
  /* A form caps itself even though the page does not — a 1300px-wide text input reads as a text
     editor, not a field. See docs/ui-rules.md. */
  flex: 1 1 200px;
  max-width: 420px;
}
</style>
