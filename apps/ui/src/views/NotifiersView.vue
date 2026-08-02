<script setup lang="ts">
/**
 * Notifiers — where events on this host get sent.
 *
 * THE FORM IS SERVER-DRIVEN. The event list and the per-type fields come from `/api/notifiers/meta`,
 * so adding a Slack or Discord type on the server puts it in this picker with its own form and no
 * change here. That is the composability seam reaching the UI; hard-coding the fields would have
 * quietly made the seam a lie.
 *
 * THE SIGNING SECRET IS SHOWN ONCE. Same discipline as a personal token and a registry credential:
 * there is no read path on the server, so this page cannot offer a reveal — it can only show the
 * value at the moment of creation and say plainly that this is the only time.
 */
import { computed, ref, watch } from 'vue';
import { api, problem } from '../api/client';
import type { DeliveryRow, NotifierMeta, NotifierRow } from '../api/types';
import { stamp } from '../composables/useFormat';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import InfoHint from '../components/InfoHint.vue';
import SkeletonList from '../components/SkeletonList.vue';
import RelativeTime from '../components/RelativeTime.vue';

const notifiers = ref<NotifierRow[]>([]);
const meta = ref<NotifierMeta | null>(null);
const loaded = ref(false);
const listError = ref('');
const unsupported = ref(false);

/** The freshly-minted secret, held only until the operator dismisses it. */
const revealed = ref<{ name: string; secret: string } | null>(null);

const form = ref({ name: '', type: 'webhook', events: [] as string[], config: {} as Record<string, string> });
const saving = ref(false);
const formError = ref('');

const openDeliveries = ref<number | null>(null);
const deliveries = ref<DeliveryRow[]>([]);

const chosenType = computed(() => meta.value?.types.find((t) => t.kind === form.value.type) ?? null);

/**
 * `config` is one bag shared by every type, so switching the picker leaves the previous type's
 * fields behind — fill a webhook URL, switch to Slack, and the POST carries a stray `url` alongside
 * `webhookUrl`. Nothing downstream rejects it: `config` is stored whole and each type validates only
 * its OWN fields, so the dead value is persisted forever and shows up in the detail view. Clearing on
 * change costs one line; unknown-key rejection on the server would cost a migration path for every
 * future type.
 */
watch(
  () => form.value.type,
  () => {
    form.value.config = {};
  },
);
const canSave = computed(
  () =>
    !saving.value &&
    !!form.value.name.trim() &&
    form.value.events.length > 0 &&
    (chosenType.value?.fields ?? []).every((f) => !f.required || !!form.value.config[f.key]?.trim()),
);

async function load(): Promise<void> {
  const [list, m] = await Promise.all([
    api.get<{ notifiers: NotifierRow[] }>('/api/notifiers'),
    api.get<NotifierMeta>('/api/notifiers/meta'),
  ]);
  loaded.value = true;
  // A server built before this feature answers 404 — a capability difference, not an error.
  if (list.status === 404 || m.status === 404) {
    unsupported.value = true;
    return;
  }
  if (!list.ok) {
    listError.value = problem(list, 'load the notifiers');
    return;
  }
  listError.value = '';
  notifiers.value = list.body.notifiers ?? [];
  if (m.ok) meta.value = m.body;
}

void load();

function toggleEvent(name: string): void {
  const at = form.value.events.indexOf(name);
  if (at === -1) form.value.events.push(name);
  else form.value.events.splice(at, 1);
}

async function create(): Promise<void> {
  saving.value = true;
  formError.value = '';
  const r = await api.post<{ notifier: NotifierRow; secret: string }>('/api/notifiers', {
    name: form.value.name.trim(),
    type: form.value.type,
    events: form.value.events,
    config: form.value.config,
  });
  saving.value = false;
  if (!r.ok) {
    formError.value = problem(r, 'register this notifier');
    return;
  }
  revealed.value = { name: r.body.notifier.name, secret: r.body.secret };
  form.value = { name: '', type: form.value.type, events: [], config: {} };
  void load();
}

async function setEnabled(n: NotifierRow, enabled: boolean): Promise<void> {
  const r = await api.patch<{ notifier: NotifierRow }>(`/api/notifiers/${n.id}`, { enabled });
  if (!r.ok) listError.value = problem(r, 'update this notifier');
  void load();
}

async function remove(n: NotifierRow): Promise<void> {
  const r = await api.del(`/api/notifiers/${n.id}`);
  if (!r.ok) {
    listError.value = problem(r, 'delete this notifier');
    return;
  }
  if (openDeliveries.value === n.id) openDeliveries.value = null;
  toast('ok', `Removed ${n.name}.`);
  void load();
}

async function test(n: NotifierRow): Promise<void> {
  const r = await api.post<{ result: { ok: boolean; status?: number; error?: string } }>(
    `/api/notifiers/${n.id}/test`,
    {},
  );
  if (!r.ok) {
    listError.value = problem(r, 'send a test delivery');
    return;
  }
  const res = r.body.result;
  // The endpoint's own answer, verbatim — "it failed" without the reason is the thing this page
  // exists to avoid.
  if (res.ok) toast('ok', `${n.name} accepted the test delivery.`);
  else toast('error', `${n.name}: ${res.error ?? `HTTP ${res.status}`}`);
  void load();
}

async function showDeliveries(n: NotifierRow): Promise<void> {
  if (openDeliveries.value === n.id) {
    openDeliveries.value = null;
    return;
  }
  const r = await api.get<{ deliveries: DeliveryRow[] }>(`/api/notifiers/${n.id}/deliveries`);
  if (!r.ok) {
    listError.value = problem(r, 'load the delivery log');
    return;
  }
  deliveries.value = r.body.deliveries ?? [];
  openDeliveries.value = n.id;
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Notifiers</h1>
        <div class="sub">
          Where events on this host get sent
          <InfoHint label="what a notifier is">
            A registration that receives events — a teardown that leaked, a deployment created, a job
            that failed. Webhooks today; the server decides what other kinds exist, and this page
            renders whatever it offers.
          </InfoHint>
        </div>
      </div>
    </div>

    <ErrorNote v-if="listError" :text="listError" title="Could not load the notifiers." />

    <section v-if="unsupported" class="panel">
      <div class="banner plain">
        <b>This server has no notifiers.</b>
        <p>It is an older build of pstack. Upgrade the host to register webhooks.</p>
      </div>
    </section>

    <template v-else>
      <!--
        Shown once, and the page says so. There is no read path on the server, so this is not a
        design choice that could be softened later — the value genuinely does not exist anywhere
        retrievable after this moment.
      -->
      <div v-if="revealed" class="banner ok">
        <b>Signing secret for “{{ revealed.name }}” — copy it now.</b>
        <p>
          This is the only time it is shown. Your receiver needs it to verify the
          <code>X-Pstack-Signature</code> header; the server keeps no way to show it again.
        </p>
        <pre class="code">{{ revealed.secret }}</pre>
        <div class="row">
          <button class="btn" @click="revealed = null">I have stored it</button>
        </div>
      </div>

      <section class="panel">
        <div class="phead">
          <h2 class="section">Registered</h2>
        </div>

        <SkeletonList v-if="!loaded" :rows="2" />
        <table v-else-if="notifiers.length" class="cards">
          <thead>
            <tr>
              <th>name</th>
              <th>events</th>
              <th>last delivery</th>
              <th></th>
            </tr>
          </thead>
          <tbody class="stagger">
            <template v-for="(n, i) in notifiers" :key="n.id">
              <tr :style="{ '--i': i }">
                <td class="name" data-label="name">
                  {{ n.name }}
                  <div class="mute" style="font-size: var(--t-sm)">
                    {{ n.type }} · {{ String(n.config.url ?? '') }}
                  </div>
                </td>
                <td data-label="events">
                  <span v-if="n.events.includes('*')" class="badge">all events</span>
                  <span v-else>{{ n.events.join(', ') }}</span>
                </td>
                <td data-label="last delivery">
                  <span v-if="!n.lastStatus" class="mute">never</span>
                  <span v-else :class="n.lastStatus === 'ok' ? 's-ok' : 's-failed'">
                    {{ n.lastStatus }}
                  </span>
                  <div v-if="n.lastAt" class="mute" style="font-size: var(--t-sm)">
                    <RelativeTime :at="n.lastAt" />
                  </div>
                </td>
                <td data-label="">
                  <div class="row" style="justify-content: flex-end">
                    <span v-if="!n.enabled" class="badge off">disabled</span>
                    <button class="ghost sm" @click="showDeliveries(n)">
                      {{ openDeliveries === n.id ? 'Hide' : 'Deliveries' }}
                    </button>
                    <button class="ghost sm" @click="test(n)">Test</button>
                    <button class="ghost sm" @click="setEnabled(n, !n.enabled)">
                      {{ n.enabled ? 'Disable' : 'Enable' }}
                    </button>
                    <ActionButton variant="danger" @click="remove(n)">Remove</ActionButton>
                  </div>
                </td>
              </tr>
              <tr v-if="openDeliveries === n.id" :key="`d${n.id}`">
                <td colspan="4">
                  <ul v-if="deliveries.length" class="kvlist">
                    <li v-for="d in deliveries" :key="d.id">
                      <span class="k">
                        <span :class="d.status === 'ok' ? 's-ok' : 's-failed'">{{ d.status }}</span>
                      </span>
                      <span class="v">
                        {{ d.event }}
                        <span class="mute" style="font-size: var(--t-sm)">
                          · {{ stamp(d.createdAt) }} · {{ d.attempts }} attempt(s)<template
                            v-if="d.responseCode"
                          >
                            · HTTP {{ d.responseCode }}</template
                          >
                        </span>
                        <div v-if="d.error" class="s-failed" style="font-size: var(--t-sm)">
                          {{ d.error }}
                        </div>
                      </span>
                    </li>
                  </ul>
                  <p v-else class="mute">Nothing delivered yet.</p>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
        <p v-else class="mute">
          Nothing registered. Events still happen — nobody is being told about them.
        </p>
      </section>

      <section class="panel">
        <h2 class="section" style="margin-bottom: var(--s3)">Add a notifier</h2>

        <div v-if="meta && meta.types.length > 1" class="field" style="max-width: 320px">
          <label for="ty">Type</label>
          <select id="ty" v-model="form.type">
            <option v-for="t in meta.types" :key="t.kind" :value="t.kind">{{ t.label }}</option>
          </select>
        </div>

        <div class="field" style="max-width: 380px">
          <label for="nn">Name</label>
          <input id="nn" v-model.trim="form.name" type="text" placeholder="ops-slack-bridge" spellcheck="false" />
        </div>

        <!-- Rendered from the server's field list — see the header. -->
        <div v-for="f in chosenType?.fields ?? []" :key="f.key" class="field" style="max-width: 380px">
          <label :for="`cfg-${f.key}`">{{ f.label }}</label>
          <input
            :id="`cfg-${f.key}`"
            v-model.trim="form.config[f.key]"
            type="text"
            :placeholder="f.placeholder"
            spellcheck="false"
          />
        </div>

        <h2 class="section" style="margin: var(--s5) 0 var(--s3)">
          Events
          <InfoHint label="which events to pick">
            <b>All events</b> also covers events added in later versions — a specific list does not,
            and the failure mode of that is silence nobody can explain. <code>job.leaked</code> is the
            one worth alerting on: a teardown ran and something survived it.
          </InfoHint>
        </h2>
        <div class="row">
          <label class="check">
            <input
              type="checkbox"
              :checked="form.events.includes('*')"
              @change="toggleEvent('*')"
            />
            All events
          </label>
        </div>
        <div v-if="!form.events.includes('*')" class="row" style="margin-top: var(--s2)">
          <label v-for="e in meta?.events ?? []" :key="e" class="check">
            <input type="checkbox" :checked="form.events.includes(e)" @change="toggleEvent(e)" />
            {{ e }}
          </label>
        </div>

        <ErrorNote v-if="formError" :text="formError" title="Could not register this notifier." />

        <div class="row" style="margin-top: var(--s4)">
          <ActionButton variant="primary" :pending="saving" :disabled="!canSave" @click="create">
            {{ saving ? 'Registering…' : 'Register' }}
          </ActionButton>
        </div>

        <p class="hint">
          Deliveries are signed with a secret shown once at registration, retried twice on failure,
          and logged here.
          <InfoHint label="how a receiver verifies a delivery">
            Recompute <code>HMAC-SHA256(secret, `${timestamp}.${rawBody}`)</code> and compare against
            <code>X-Pstack-Signature</code>, using the <em>raw</em> body — re-serialising it changes
            the bytes. Reject anything whose <code>X-Pstack-Timestamp</code> is more than five minutes
            old, and dedupe on <code>X-Pstack-Delivery</code>: delivery is at-least-once, and that id
            is stable across retries.
          </InfoHint>
        </p>
      </section>
    </template>
  </div>
</template>
