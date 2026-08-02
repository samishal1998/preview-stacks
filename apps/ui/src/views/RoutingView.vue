<script setup lang="ts">
/**
 * Traefik's dynamic configuration — the file list, and an editor.
 *
 * WHAT LIVES HERE. Traefik reads config from two providers: labels on containers (every per-PR router)
 * and a watched *directory* of files. This page is the directory: middleware (basic auth, rate limits,
 * IP allow-lists), TLS options, the catch-all fallback router, routes to something running outside
 * compose. Several files rather than one is the point of a watched directory — one file per concern.
 *
 * THE WARNING IS NOT DECORATION. Traefik's documented behaviour is that an unparseable file produces
 * a parse error for the whole DIRECTORY, and the rest can be discarded with it — so a careless save
 * here breaks *other people's* routes, not the file being edited. The server validates before writing
 * and renames into place so the watcher never sees a partial file, and every rejection is rendered
 * verbatim because it names the actual problem.
 *
 * What it cannot break: `control.<domain>` and `api.<domain>` are docker labels on the pstack
 * container, not file config. This page cannot lock you out of the page you would use to undo a bad
 * save. That is stated in the UI, because an operator hesitating over a save deserves to know it.
 *
 * `previous` from a save is an in-session undo. There is deliberately no history on disk: the obvious
 * place to keep it is the one directory that must contain nothing but dynamic config.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import type {
  LiveRoutesResponse,
  RoutingFile,
  RoutingListResponse,
  RoutingReadResponse,
  RoutingWriteResponse,
  RuntimeRoute,
} from '../api/types';
import { settings } from '../composables/useSettings';
import { stamp } from '../composables/useFormat';
import { toast } from '../composables/useToasts';
import ActionButton from '../components/ActionButton.vue';
import ErrorNote from '../components/ErrorNote.vue';
import InfoHint from '../components/InfoHint.vue';
import SkeletonList from '../components/SkeletonList.vue';

/**
 * The live routes, from container labels.
 *
 * This page used to show only the file provider, and the reported confusion was exactly right: a
 * deployment goes up, its hostname appears, and nothing here changes — because per-PR routers are
 * DOCKER labels and the files below are Traefik's *other* provider. Showing both is what makes the
 * page match what "routing" means to whoever opened it.
 */
const live = ref<RuntimeRoute[]>([]);
const liveReachable = ref<boolean | null>(null);

const files = ref<RoutingFile[]>([]);
const dir = ref('');
const writable = ref<boolean | null>(null);
const loaded = ref(false);
const listError = ref('');

/** The open file. `null` name means a new one. */
const openName = ref<string | null>(null);
const draft = ref('');
const original = ref('');
const withheld = ref(false);
const loadingFile = ref(false);
const saving = ref(false);
const fileError = ref('');
const newName = ref('');
/** Last replaced content, offered as an undo for the rest of the session. */
const undo = ref<{ name: string; content: string } | null>(null);
const confirmDelete = ref('');

const dirty = computed(() => openName.value !== null && draft.value !== original.value);
const canSave = computed(
  () => !!settings.token && !saving.value && draft.value.trim().length > 0 && (dirty.value || openName.value === ''),
);

const STARTER = `http:
  middlewares:
    # An example — replace it. Middlewares go UNDER http; at the top level Traefik ignores them.
    ip-allow:
      ipAllowList:
        sourceRange:
          - 203.0.113.0/24
`;

async function loadList(): Promise<void> {
  const r = await api.get<RoutingListResponse>('/api/routing');
  loaded.value = true;
  if (!r.ok) {
    listError.value = problem(r, 'load the routing files');
    return;
  }
  listError.value = '';
  files.value = r.body.files ?? [];
  dir.value = r.body.dir ?? '';
  writable.value = r.body.writable ?? false;
}

async function loadLive(): Promise<void> {
  const r = await api.get<LiveRoutesResponse>('/api/routing/live');
  if (!r.ok) {
    liveReachable.value = false;
    return;
  }
  liveReachable.value = r.body.reachable;
  live.value = r.body.routes ?? [];
}

void loadList();
void loadLive();

async function open(name: string): Promise<void> {
  openName.value = name;
  loadingFile.value = true;
  fileError.value = '';
  withheld.value = false;
  confirmDelete.value = '';
  // Authenticated: dynamic config holds basic-auth hashes and forward-auth URLs.
  const r = await api.getAuthed<RoutingReadResponse>(`/api/routing/${encodeURIComponent(name)}`);
  loadingFile.value = false;
  if (!r.ok) {
    fileError.value = problem(r, 'open this file');
    return;
  }
  if (r.body.sourceWithheld) {
    withheld.value = true;
    draft.value = '';
    original.value = '';
    return;
  }
  draft.value = r.body.content ?? '';
  original.value = draft.value;
}

function startNew(): void {
  openName.value = '';
  newName.value = '';
  draft.value = STARTER;
  original.value = '';
  withheld.value = false;
  fileError.value = '';
}

function close(): void {
  openName.value = null;
  draft.value = '';
  original.value = '';
  fileError.value = '';
}

async function save(): Promise<void> {
  const name = openName.value === '' ? newName.value.trim() : openName.value;
  if (!name) {
    fileError.value = 'Give the file a name, ending in .yml';
    return;
  }
  saving.value = true;
  fileError.value = '';
  const r = await api.put<RoutingWriteResponse>(`/api/routing/${encodeURIComponent(name)}`, {
    content: draft.value,
  });
  saving.value = false;
  if (!r.ok) {
    // Rendered verbatim: the server's message names the unknown section or the YAML error, which is
    // the whole diagnosis.
    fileError.value = problem(r, 'save this file');
    return;
  }
  if (r.body.previous) undo.value = { name, content: r.body.previous };
  original.value = draft.value;
  openName.value = name;
  toast('ok', `Saved ${name}. Traefik reloads within a second or two.`);
  void loadList();
}

async function revert(): Promise<void> {
  const u = undo.value;
  if (!u) return;
  draft.value = u.content;
  openName.value = u.name;
  await save();
  undo.value = null;
}

async function remove(): Promise<void> {
  const name = openName.value;
  if (!name || confirmDelete.value !== name) return;
  const r = await api.del<RoutingWriteResponse>(`/api/routing/${encodeURIComponent(name)}`);
  if (!r.ok) {
    fileError.value = problem(r, 'delete this file');
    return;
  }
  if (r.body.previous) undo.value = { name, content: r.body.previous };
  toast('ok', `Deleted ${name}.`);
  close();
  void loadList();
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Routing</h1>
        <div class="sub">
          Every route on this host, and the config files behind them
          <InfoHint label="what belongs in these files">
            Middleware (basic auth, rate limits, IP allow-lists), TLS options, the catch-all fallback
            router, and routes to anything running outside compose. Per-PR routers are not here —
            those are labels in each deployment's own compose file.
          </InfoHint>
        </div>
      </div>
      <span class="grow" />
      <button v-if="writable" class="btn primary" @click="startNew">New file</button>
    </div>

    <ErrorNote v-if="listError" :text="listError" title="Could not load the routing files." />

    <div v-if="undo" class="banner info">
      <b>Replaced {{ undo.name }}.</b>
      <p>
        The previous contents are still here for this session.
        <button class="btn sm" @click="revert">Put it back</button>
      </p>
    </div>

    <!-- ============================ live routes ============================ -->
    <section class="panel">
      <div class="phead">
        <h2 class="section">Live routes</h2>
        <span class="grow" />
        <span class="mute" style="font-size: var(--t-sm)">
          from container labels
          <InfoHint label="why routes are not in the files below" align="end">
            Traefik reads two providers. Per-PR routers are <b>labels on containers</b>, written by each
            deployment's own compose file — so deploying a stack changes this list and never the files.
            The files are the other provider: middleware, TLS options, the fallback router.
          </InfoHint>
        </span>
      </div>

      <div v-if="liveReachable === false" class="banner warn">
        <b>Docker did not answer.</b>
        <p>Routes could not be listed — which is not the same as there being none.</p>
      </div>

      <table v-else-if="live.length" class="cards">
        <thead>
          <tr>
            <th>url</th>
            <th>forwards to</th>
            <th>deployment</th>
            <th>router</th>
          </tr>
        </thead>
        <tbody class="stagger">
          <tr v-for="(r, i) in live" :key="`${r.container}-${r.router}`" :style="{ '--i': i }">
            <td data-label="url">
              <div v-for="h in r.hosts" :key="h">
                <a v-if="!h.startsWith('(pattern)')" :href="`https://${h}`" target="_blank" rel="noreferrer">
                  {{ h }}
                </a>
                <span v-else class="mono mute">{{ h }}</span>
              </div>
              <span v-if="!r.hosts.length" class="mute">no host in the rule</span>
            </td>
            <td data-label="forwards to">
              <span v-if="r.target" class="mono">{{ r.target }}</span>
              <span v-else class="s-failed">
                {{ r.port === null ? 'no port declared' : 'not on the ingress network' }}
              </span>
            </td>
            <td data-label="deployment">
              <RouterLink v-if="r.project" :to="`/deployments/${encodeURIComponent(r.project)}/runtime`">
                {{ r.project }}
              </RouterLink>
              <span v-else class="mute">—</span>
            </td>
            <td class="dim" data-label="router">{{ r.router }}</td>
          </tr>
        </tbody>
      </table>

      <p v-else class="mute">
        No container on this host declares a Traefik router. A deployment's routes come from labels in
        its own compose file — open a deployment's <b>Containers &amp; routes</b> tab to see what is
        missing.
      </p>
    </section>

    <!-- ============================ config files ============================ -->
    <section class="panel">
      <div class="phead">
        <h2 class="section">Config files</h2>
        <span class="grow" />
        <span v-if="dir" class="mute" style="font-size: var(--t-sm)">
          <code>{{ dir }}</code>
        </span>
      </div>

    <!--
        The blast radius, stated once and up front — and the reassurance alongside it, because "this can
        break everything" without "you will still be able to fix it" just makes people avoid the page.
      -->
      <div class="banner warn">
        <b>A bad file here affects every preview on this host.</b>
        <p>
          Traefik reads this directory as a whole: one unparseable file and it logs a parse error for the
          <em>directory</em>, so the symptom is other routes disappearing. Saves are checked before they
          are written and land atomically, so a rejected file never reaches Traefik.
          <InfoHint label="what this page cannot break">
            This page and the API are routed by container labels, not by these files — so no save here
            can lock you out of the page you would use to undo it.
          </InfoHint>
        </p>
      </div>

      <div v-if="writable === false" class="banner failed">
        <b>Read-only: the API cannot write to this directory.</b>
        <p>
          The control stack mounts it into the API from 0.4.0 onward. Re-run
          setup again on the host to pick up the mount — it is safe to repeat, and it recreates
          the control containers.
        </p>
      </div>

      <div v-else-if="!settings.token" class="banner info">
        <b>Viewing needs a token too.</b>
        <p>
          These files hold basic-auth hashes and forward-auth URLs, so their contents are not served
          unauthenticated. <RouterLink to="/settings">Add your token</RouterLink>.
        </p>
      </div>

      <SkeletonList v-if="!loaded" :rows="3" />
      <ul v-else-if="files.length" class="kvlist">
        <li v-for="f in files" :key="f.name">
          <span class="k">
            <button class="ghost sm" @click="open(f.name)">{{ f.name }}</button>
          </span>
          <span class="v mute" style="font-size: var(--t-sm)">
            {{ f.size }} bytes · {{ stamp(f.updatedAt) }}
          </span>
        </li>
      </ul>
      <p v-else class="mute">
        No files yet. Everything is routed by container labels, which is a perfectly good place to be.
      </p>
    </section>

    <section v-if="openName !== null" class="panel">
      <div class="phead">
        <h2 class="section">{{ openName === '' ? 'New file' : openName }}</h2>
        <span v-if="dirty" class="badge warn">unsaved</span>
        <span class="grow" />
        <button class="ghost sm" @click="close">Close</button>
      </div>

      <div v-if="openName === ''" class="field" style="max-width: 340px">
        <label for="fname">File name</label>
        <input
          id="fname"
          v-model.trim="newName"
          type="text"
          placeholder="middleware.yml"
          spellcheck="false"
          autocomplete="off"
        />
        <div class="mute hint">Lower case, ending in <code>.yml</code>.</div>
      </div>

      <SkeletonList v-if="loadingFile" :rows="4" />

      <div v-else-if="withheld" class="banner plain">
        <b>Hidden without an access token.</b>
        <p>
          <RouterLink to="/settings">Add your token</RouterLink> to read and edit this file.
        </p>
      </div>

      <template v-else>
        <div class="field">
          <label for="content">Contents</label>
          <textarea id="content" v-model="draft" rows="18" spellcheck="false" />
        </div>

        <ErrorNote v-if="fileError" :text="fileError" title="Traefik would not accept this." />

        <div class="row" style="margin-top: var(--s4)">
          <ActionButton variant="primary" :pending="saving" :disabled="!canSave" @click="save">
            {{ saving ? 'Saving…' : 'Save' }}
          </ActionButton>
          <span v-if="!settings.token" class="mute">Saving needs a token.</span>
        </div>

        <template v-if="openName">
          <h2 class="section" style="margin: var(--s5) 0 var(--s3)">Delete this file</h2>
          <p class="dim">
            Whatever it configures stops applying as soon as Traefik reloads. If it holds a router,
            those hostnames stop resolving to anything.
          </p>
          <div class="field" style="max-width: 340px">
            <label :for="`confirm-${openName}`">
              Type <b>{{ openName }}</b> to confirm
            </label>
            <input
              :id="`confirm-${openName}`"
              v-model.trim="confirmDelete"
              type="text"
              :placeholder="openName"
              spellcheck="false"
              autocomplete="off"
            />
          </div>
          <div class="row">
            <ActionButton
              variant="danger"
              :disabled="confirmDelete !== openName || !settings.token"
              :title="confirmDelete !== openName ? 'Type the file name to confirm.' : undefined"
              @click="remove"
            >
              Delete
            </ActionButton>
          </div>
        </template>
      </template>
    </section>
  </div>
</template>
