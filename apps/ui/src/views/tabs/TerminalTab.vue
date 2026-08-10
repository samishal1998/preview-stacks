<script setup lang="ts">
/**
 * A shell inside one of this deployment's containers.
 *
 * THE SERVER DECIDES WHICH CONTAINER. This picker is filled from `/runtime`, i.e. the containers the
 * deployment actually owns, and the server checks the name again on upgrade — a hand-typed
 * `?container=pstack-control` is a 404 there, not here. The picker is a convenience, never the
 * boundary.
 *
 * THE COOKIE IS THE CREDENTIAL. A browser cannot set an `Authorization` header on a WebSocket, which
 * is exactly why sessions are cookies (0.10.0) — the handshake is a same-origin GET and carries the
 * session automatically. There is no token in this URL and there must never be one: a URL lands in
 * proxy logs and browser history.
 *
 * NO PTY. The server runs `docker exec -i`, not `-it`, so there is no prompt, no ^C and no `top`.
 * That is stated on the page rather than left for the operator to discover as "the terminal is
 * broken" — see the banner below and the comment at the head of `terminal.ts`.
 */
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRoute } from 'vue-router';
import { api, problem } from '../../api/client';
import type { RuntimeResponse } from '../../api/types';
import ErrorNote from '../../components/ErrorNote.vue';
import InfoHint from '../../components/InfoHint.vue';
import SelectMenu from '../../components/SelectMenu.vue';

const route = useRoute();
const id = computed(() => String(route.params.id));

const SHELLS = ['sh', 'bash', 'ash', 'zsh', 'fish'];

const rt = ref<RuntimeResponse | null>(null);
const loadError = ref('');
const container = ref('');
const shell = ref('sh');
const state = ref<'idle' | 'connecting' | 'open' | 'closed'>('idle');
const closeReason = ref('');

const host = ref<HTMLDivElement | null>(null);
/** shallowRef: xterm's Terminal is a large mutable object and must not be made reactive. */
const term = shallowRef<import('@xterm/xterm').Terminal | null>(null);
const fit = shallowRef<import('@xterm/addon-fit').FitAddon | null>(null);
const socket = shallowRef<WebSocket | null>(null);
let observer: ResizeObserver | null = null;

const containerOptions = computed(() =>
  running.value.map((c) => ({ value: c.name, label: c.name, hint: c.service ?? undefined })),
);

const running = computed(() => rt.value?.containers.filter((c) => c.state === 'running') ?? []);

async function loadContainers() {
  const r = await api.get<RuntimeResponse>(
    `/api/deployments/${encodeURIComponent(id.value)}/runtime`,
  );
  if (!r.ok) {
    loadError.value = problem(r, "list this deployment's containers");
    return;
  }
  loadError.value = '';
  rt.value = r.body;
  if (container.value) return;
  /*
   * `?container=` is how the Containers table jumps straight to a shell here. Honoured only when it
   * names a container this deployment actually owns and is running — an unknown name falls back to
   * the first, because the alternative is a picker sitting on a container that does not exist and a
   * Connect button that 404s. (The server checks the name again on upgrade; this is convenience.)
   */
  const wanted = typeof route.query.container === 'string' ? route.query.container : '';
  const match = running.value.find((c) => c.name === wanted);
  container.value = match?.name ?? running.value[0]?.name ?? '';
}
void loadContainers();

async function connect() {
  if (state.value === 'connecting' || state.value === 'open') return;
  closeReason.value = '';
  state.value = 'connecting';

  // Loaded on demand, not in the app shell: xterm is ~250KB and only this tab needs it.
  const [{ Terminal }, { FitAddon }] = await Promise.all([
    import('@xterm/xterm'),
    import('@xterm/addon-fit'),
  ]);
  await import('@xterm/xterm/css/xterm.css');

  term.value?.dispose();
  const t = new Terminal({
    // The OUTPUT half of the line discipline the missing pty would have provided. A real terminal
    // converts NL to CR-NL on the way out (ONLCR); without it every `\n` moves DOWN a row without
    // returning the carriage, and `ls` renders as a staircase marching off the right edge. The input
    // half — CR to NL — is done server-side; see `crToNl` in api.ts.
    convertEol: true,
    fontSize: 13,
    fontFamily: 'var(--font-mono, ui-monospace, monospace)',
    cursorBlink: true,
    theme: { background: '#0b0e14' },
  });
  const f = new FitAddon();
  t.loadAddon(f);
  if (host.value) {
    host.value.innerHTML = '';
    t.open(host.value);
    f.fit();
  }
  term.value = t;
  fit.value = f;

  observer?.disconnect();
  observer = new ResizeObserver(() => {
    try {
      f.fit();
    } catch {
      /* the element is detached mid-transition */
    }
  });
  if (host.value) observer.observe(host.value);

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const qs = new URLSearchParams({ container: container.value, shell: shell.value });
  const ws = new WebSocket(
    `${proto}://${location.host}/api/deployments/${encodeURIComponent(id.value)}/terminal?${qs}`,
  );
  ws.binaryType = 'arraybuffer';
  socket.value = ws;

  ws.onopen = () => {
    state.value = 'open';
    t.focus();
  };
  // Bytes, not text: decoding per frame would corrupt a multi-byte character split across two
  // reads. xterm takes a Uint8Array and handles the boundary itself.
  ws.onmessage = (e) =>
    t.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data as ArrayBuffer));
  ws.onclose = (e) => {
    state.value = 'closed';
    // A close CODE alone tells an operator nothing. 1006 in particular is the browser's "it just
    // went away", which here almost always means the upgrade was refused.
    closeReason.value =
      e.reason || (e.code === 1006 ? 'the connection dropped (the upgrade may have been refused)' : `code ${e.code}`);
  };
  ws.onerror = () => {
    if (state.value === 'connecting') closeReason.value = 'could not open the connection';
  };
  t.onData((d) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(d);
  });
}

function disconnect() {
  socket.value?.close(1000, 'closed by the operator');
}

function teardown() {
  observer?.disconnect();
  observer = null;
  socket.value?.close();
  socket.value = null;
  term.value?.dispose();
  term.value = null;
}

// Switching container or shell must not leave the old shell running against a dead view.
watch([container, shell], () => {
  if (state.value === 'open' || state.value === 'connecting') disconnect();
});
watch(id, () => {
  teardown();
  state.value = 'idle';
  void loadContainers();
});
onBeforeUnmount(teardown);
</script>

<template>
  <section class="stack">
    <div class="banner warn">
      <b>No TTY.</b> The shell runs without a terminal device, so there is no prompt, no
      <code>Ctrl-C</code>, and no full-screen programs (<code>top</code>, <code>vim</code>). Type a
      command and press Enter.
      <InfoHint label="why">
        A pseudo-terminal needs a wrapper (<code>script</code>) whose flags differ across
        distributions, and it was never verified on a real host — so pstack ships the half it can
        stand behind. Everything an operator usually opens a shell for (<code>ls</code>,
        <code>cat</code>, <code>env</code>, <code>psql</code>, a migration) works.
      </InfoHint>
    </div>

    <ErrorNote v-if="loadError" :text="loadError" title="Could not list this deployment's containers." />

    <div class="row wrap" style="gap: var(--s3); align-items: end">
      <label class="field">
        <span>Container</span>
        <SelectMenu v-model="container" label="Container" :disabled="state === 'open' || state === 'connecting'" :options="containerOptions" />
      </label>
      <label class="field">
        <span>Shell</span>
        <SelectMenu v-model="shell" label="Shell" :disabled="state === 'open' || state === 'connecting'" :options="SHELLS.map((s) => ({ value: s, label: s }))" />
      </label>
      <button
        v-if="state !== 'open'"
        class="primary"
        :disabled="!container || state === 'connecting'"
        @click="connect"
      >
        {{ state === 'connecting' ? 'Connecting…' : 'Open shell' }}
      </button>
      <button v-else @click="disconnect">Close shell</button>
      <span class="grow" />
      <span v-if="state === 'open'" class="badge ok"><span class="dot pulse" />connected</span>
    </div>

    <p v-if="!running.length && !loadError" class="hint">
      Nothing is running for this deployment, so there is nothing to open a shell in.
    </p>

    <p v-if="state === 'closed' && closeReason" class="hint">Session ended — {{ closeReason }}.</p>

    <div ref="host" class="terminal-host" :data-state="state" />

    <p class="hint">
      Every session is recorded — who, which container, when — and the record is written when the
      shell opens, so a session that dies with the process still leaves one.
    </p>
  </section>
</template>

<style scoped>
.terminal-host {
  min-height: 26rem;
  border: 1px solid var(--line);
  border-radius: var(--r3);
  background: #0b0e14;
  padding: var(--s2);
  overflow: hidden;
}
.terminal-host[data-state='idle'] {
  display: none;
}
</style>
