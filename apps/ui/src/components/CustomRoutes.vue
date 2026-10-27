<script setup lang="ts">
/**
 * Custom routes: every route (the Routing page), or the ones one deployment holds (its Containers &
 * routes tab). One component, so both pages ask the same move question the same way.
 *
 * A route another stack holds is a MOVE, and moving takes it away from that stack: the Assign button
 * becomes the armed confirm ("Move checkout from a to b?"), and the second click sends one
 * `PUT … {force: true}`. A list that was stale gets the same question back from the server as a 409
 * carrying `assignedTo`, and that re-arms the button rather than surfacing as an error.
 *
 * Writes are hidden below developer as a courtesy; the server's permission table decides.
 */
import { computed, nextTick, ref, useId, watch } from 'vue';
import { api, problem } from '../api/client';
import type { CustomRoute, RouteHeld } from '../api/types';
import { can } from '../composables/useAuth';
import { state } from '../composables/useControlPlane';
import { focusLost, hostItems, moveQuestion, needsMove, type Holder } from '../composables/customRoutes';
import { toast } from '../composables/useToasts';
import ActionButton from './ActionButton.vue';
import ErrorNote from './ErrorNote.vue';
import SelectMenu, { type SelectOption } from './SelectMenu.vue';
import SkeletonList from './SkeletonList.vue';

/** Set on the deployment page: only its routes, and Assign targets it. */
const props = defineProps<{ deployment?: string }>();

const routes = ref<CustomRoute[]>([]);
const loaded = ref(false);
const listError = ref('');
/** The route name a request is in flight for. */
const busy = ref('');

const name = ref('');
const target = ref(props.deployment ?? '');
const formError = ref('');
/** The holder a 409 named when the list said the route was free. */
const stale = ref<Holder | null>(null);
const submitBtn = ref<InstanceType<typeof ActionButton> | null>(null);
const nameInput = ref<HTMLInputElement | null>(null);
const ids = useId();

const mayWrite = computed(() => can('developer'));
const shown = computed(() =>
  props.deployment ? routes.value.filter((r) => r.deployment === props.deployment) : routes.value,
);
const typed = computed(() => name.value.trim());
const existing = computed(() => routes.value.find((r) => r.name === typed.value));
const stackOf = (id: string): string => state.deployments.find((d) => d.id === id)?.stack ?? id;

const holder = computed(() => stale.value ?? (existing.value && target.value ? needsMove(existing.value, target.value) : null));
const question = computed(() => (holder.value ? moveQuestion(typed.value, holder.value, stackOf(target.value)) : undefined));
const why = computed(() => {
  if (!typed.value) return 'Name the route.';
  if (existing.value && !target.value) return 'Pick a deployment.';
  if (existing.value && existing.value.deployment === target.value) return 'Already assigned.';
  return undefined;
});

const targetOptions = computed<SelectOption[]>(() => [
  { value: '', label: 'Unassigned' },
  ...state.deployments.map((d) => ({ value: d.id, label: d.id, hint: d.stack && d.stack !== d.id ? d.stack : undefined })),
]);

watch([name, target], () => (stale.value = null));
watch(
  () => props.deployment,
  (d) => (target.value = d ?? ''),
);

const path = (n: string): string => `/api/custom-routes/${encodeURIComponent(n)}`;

async function load(): Promise<void> {
  const r = await api.get<CustomRoute[]>('/api/custom-routes');
  loaded.value = true;
  if (!r.ok) {
    listError.value = problem(r, 'load custom routes');
    return;
  }
  listError.value = '';
  routes.value = Array.isArray(r.body) ? r.body : [];
}
void load();
defineExpose({ load });

/**
 * After a write: the list, then focus. A pending button is natively disabled and Unassign leaves with
 * its row, so a settled write would leave focus on <body>; land on Route instead.
 */
async function settle(): Promise<void> {
  await load();
  await nextTick();
  if (focusLost(document.activeElement, document.body)) nameInput.value?.focus();
}

/** Creates the route when it is new, else points it at the target. `force` is the confirmed move. */
async function submit(force: boolean): Promise<void> {
  const n = typed.value;
  const t = target.value;
  const isNew = !existing.value;
  formError.value = '';
  busy.value = n;
  const r = isNew
    ? await api.post<CustomRoute>('/api/custom-routes', t ? { name: n, deployment: t } : { name: n })
    : await api.put<CustomRoute & RouteHeld>(`${path(n)}/assignment`, force ? { deployment: t, force } : { deployment: t });
  busy.value = '';
  void settle();
  if (r.ok) {
    toast('ok', !t ? `Created ${n}.` : force ? `Moved ${n} to ${stackOf(t)}.` : `Assigned ${n} to ${stackOf(t)}.`);
    name.value = '';
    if (!props.deployment) target.value = '';
    return;
  }
  // Someone else took it since the list loaded: that is the move question, not an error.
  const held = !force && r.status === 409 ? needsMove(r.body, t) : null;
  if (held) {
    stale.value = held;
    await nextTick();
    submitBtn.value?.arm();
    return;
  }
  formError.value = problem(r, isNew ? `create ${n}` : `assign ${n}`);
}

async function drop(r: CustomRoute, kind: 'unassign' | 'delete'): Promise<void> {
  busy.value = r.name;
  const res = await api.del(kind === 'delete' ? path(r.name) : `${path(r.name)}/assignment`);
  busy.value = '';
  if (res.ok) toast('ok', kind === 'delete' ? `Deleted ${r.name}.` : `Unassigned ${r.name}.`);
  else toast('error', problem(res, `${kind} ${r.name}`));
  void settle();
}
</script>

<template>
  <section class="panel">
    <div class="phead">
      <h2 class="section">Custom routes</h2>
    </div>

    <ErrorNote v-if="listError" :text="listError" />
    <SkeletonList v-else-if="!loaded" :rows="2" />
    <table v-else-if="shown.length" role="table" class="cards">
      <thead role="rowgroup">
        <tr role="row">
          <th role="columnheader">Name</th>
          <th role="columnheader">Hosts</th>
          <th v-if="!deployment" role="columnheader">Deployment</th>
          <th v-if="mayWrite" role="columnheader" aria-label="Actions" />
        </tr>
      </thead>
      <tbody role="rowgroup">
        <tr v-for="r in shown" :key="r.name" role="row">
          <td role="cell" class="mono" data-label="name">{{ r.name }}</td>
          <td role="cell" class="cell-wrap" data-label="hosts">
            <!-- One value column: a card row's cell is a flex row, and each host would be its own column. -->
            <div>
              <div v-for="h in hostItems(r.hosts)" :key="h.host">
                <a v-if="h.href" :href="h.href" target="_blank" rel="noreferrer">{{ h.host }}</a>
                <span v-else class="mono mute">{{ h.host }}</span>
              </div>
              <span v-if="!r.hosts.length" class="mute">—</span>
            </div>
          </td>
          <td v-if="!deployment" role="cell" data-label="deployment">
            <RouterLink v-if="r.deployment" :to="`/deployments/${encodeURIComponent(r.deployment)}/runtime`">
              {{ r.stack ?? r.deployment }}
            </RouterLink>
            <span v-else class="mute">Unassigned</span>
          </td>
          <td v-if="mayWrite" role="cell" class="row-actions" data-label="">
            <ActionButton
              v-if="r.deployment"
              class="ghost sm"
              :pending="busy === r.name"
              :disabled="!!busy"
              :confirm="`Unassign ${r.name}?`"
              @run="drop(r, 'unassign')"
              >Unassign</ActionButton
            >
            <ActionButton
              v-if="!deployment"
              class="danger sm"
              :pending="busy === r.name"
              :disabled="!!busy"
              :confirm="`Delete ${r.name}?`"
              @run="drop(r, 'delete')"
              >Delete</ActionButton
            >
          </td>
        </tr>
      </tbody>
    </table>
    <p v-else class="mute">{{ deployment ? 'No routes assigned.' : 'No custom routes.' }}</p>

    <div v-if="mayWrite && loaded" class="cr-form">
      <div class="row wrap">
        <div class="field">
          <label :for="`${ids}-name`">Route</label>
          <input
            :id="`${ids}-name`"
            ref="nameInput"
            v-model="name"
            type="text"
            :list="`${ids}-names`"
            placeholder="checkout"
            spellcheck="false"
            autocomplete="off"
          />
          <datalist :id="`${ids}-names`">
            <option v-for="r in routes" :key="r.name" :value="r.name" />
          </datalist>
        </div>
        <div v-if="!deployment" class="field">
          <label :for="`${ids}-target`">Deployment</label>
          <SelectMenu :id="`${ids}-target`" v-model="target" label="Deployment" :options="targetOptions" />
        </div>
      </div>
      <div class="row">
        <ActionButton
          ref="submitBtn"
          variant="primary"
          :pending="!!busy && busy === typed"
          :disabled="!!busy || !!why"
          :title="why"
          :confirm="question"
          @click="question || submit(false)"
          @run="submit(true)"
        >
          {{ existing || deployment ? 'Assign' : 'Create' }}
        </ActionButton>
      </div>
    </div>
    <ErrorNote v-if="formError" :text="formError" />
  </section>
</template>

<style scoped>
.cr-form {
  margin-top: var(--s4);
  display: flex;
  flex-direction: column;
  gap: var(--s3);
}
/* Side by side where there is room, stacked where there is not. */
.cr-form .field {
  flex: 1 1 200px;
}
</style>
