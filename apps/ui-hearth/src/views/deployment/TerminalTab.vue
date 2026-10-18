<script setup lang="ts">
/**
 * A shell inside one of this deployment's containers, over the terminal WebSocket.
 *
 * The server decides which container: this picker lists what /runtime says the deployment owns, and
 * the server checks the name again on upgrade. The session cookie authenticates the handshake (a
 * browser cannot set a header on a WebSocket), so no token is ever put in the URL.
 *
 * No pty: the server runs `docker exec -i`, so there is no prompt and no Ctrl-C. Said on the page.
 */
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { useRoute } from 'vue-router';
import { HAlert, HBadge, HButton, HSelect, HStack, HText } from '@hearth-ui/vue';
import type { DeploymentRow, Runtime } from '@samyx/preview-stacks-client';
import '@xterm/xterm/css/xterm.css';
import { terminalUrl } from '../../api-deployment';

const props = defineProps<{ id: string; dep: DeploymentRow | null; runtime: Runtime | null; error?: string }>();
const route = useRoute();

const SHELLS = ['sh', 'bash', 'ash', 'zsh', 'fish'].map((s) => ({ value: s, label: s }));
type Phase = 'idle' | 'connecting' | 'open' | 'closed';

const container = ref('');
const shell = ref('sh');
const phase = ref<Phase>('idle');
const closeReason = ref('');

const host = ref<HTMLDivElement | null>(null);
// shallowRef: xterm's Terminal is a large mutable object and must not be made reactive.
const term = shallowRef<import('@xterm/xterm').Terminal | null>(null);
const socket = shallowRef<WebSocket | null>(null);
let observer: ResizeObserver | null = null;
// Bumped by teardown: a connect() that was awaiting the xterm import sees it moved and stops.
let gen = 0;

/** Only containers a shell can reach: running, on this node, not a one-shot job. */
const reachable = computed(() => (props.runtime?.containers ?? []).filter((c) => c.state === 'running' && !c.remote && !c.job));
const options = computed(() => reachable.value.map((c) => ({ value: c.name, label: c.service ? `${c.service} (${c.name})` : c.name })));
const live = computed(() => phase.value === 'open' || phase.value === 'connecting');

// ?container= is how the Containers table jumps here. Honoured only for a container this deployment
// owns and runs; anything else falls back to the first, so the picker never sits on a name that 404s.
watch(
  reachable,
  (list) => {
    if (container.value && list.some((c) => c.name === container.value)) return;
    const wanted = typeof route.query.container === 'string' ? route.query.container : '';
    container.value = list.find((c) => c.name === wanted)?.name ?? list[0]?.name ?? '';
  },
  { immediate: true },
);

async function connect(): Promise<void> {
  if (live.value || !container.value) return;
  closeReason.value = '';
  phase.value = 'connecting';
  const mine = gen;
  // Loaded on demand: xterm is large and only this tab needs it.
  const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')]);
  if (mine !== gen) return;
  term.value?.dispose();
  const t = new Terminal({
    // The output half of the line discipline a missing pty would provide: NL -> CR-NL, or `ls` staircases.
    convertEol: true,
    fontSize: 13,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
    cursorBlink: true,
    theme: { background: '#0b0e14' },
  });
  const fit = new FitAddon();
  t.loadAddon(fit);
  if (host.value) {
    host.value.replaceChildren();
    t.open(host.value);
    fit.fit();
  }
  term.value = t;
  observer?.disconnect();
  observer = new ResizeObserver(() => {
    try {
      fit.fit();
    } catch {
      /* detached mid-transition */
    }
  });
  if (host.value) observer.observe(host.value);

  const ws = new WebSocket(terminalUrl(props.id, container.value, shell.value));
  ws.binaryType = 'arraybuffer';
  socket.value = ws;
  ws.onopen = () => {
    phase.value = 'open';
    t.focus();
  };
  // Bytes, not text: decoding per frame would corrupt a multi-byte character split across reads.
  ws.onmessage = (e) => t.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data as ArrayBuffer));
  ws.onclose = (e) => {
    phase.value = 'closed';
    // 1006 is the browser's "it went away", which here almost always means the upgrade was refused.
    closeReason.value = e.reason || (e.code === 1006 ? 'connection dropped' : `code ${e.code}`);
  };
  ws.onerror = () => {
    if (phase.value === 'connecting') closeReason.value = 'could not connect';
  };
  t.onData((d) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(d);
  });
}

function disconnect(): void {
  if (!socket.value) {
    // Still loading xterm: cancel the pending connect.
    gen++;
    phase.value = 'idle';
    return;
  }
  socket.value.close(1000, 'closed by the operator');
}

function teardown(): void {
  gen++;
  if (socket.value) socket.value.onclose = socket.value.onmessage = socket.value.onerror = socket.value.onopen = null;
  observer?.disconnect();
  observer = null;
  socket.value?.close();
  socket.value = null;
  term.value?.dispose();
  term.value = null;
}

// Changing container or shell must not leave the old shell running against a stale picker.
watch([container, shell], () => {
  if (live.value) disconnect();
});
watch(
  () => props.id,
  () => {
    teardown();
    phase.value = 'idle';
    closeReason.value = '';
  },
);
onBeforeUnmount(teardown);
</script>

<template>
  <HStack direction="column" :gap="3">
    <HAlert tone="warning" title="No TTY" description="No prompt, no Ctrl-C, no full-screen programs." compact />

    <HStack direction="row" :gap="3" wrap align="end">
      <HSelect v-model="container" label="Container" :options="options" :disabled="live || !options.length" :placeholder="options.length ? undefined : (runtime ? 'No container' : 'Loading')" />
      <HSelect v-model="shell" label="Shell" :options="SHELLS" :disabled="live" />
      <HButton v-if="phase !== 'open'" label="Open shell" :loading="phase === 'connecting'" :disabled="!container" @click="connect" />
      <HButton v-else variant="secondary" label="Close shell" @click="disconnect" />
      <HBadge v-if="phase === 'open'" dot tone="success" label="Connected" />
    </HStack>

    <HAlert v-if="error" tone="danger" title="Could not list containers" :description="error" />
    <HText v-else-if="!reachable.length" tone="muted">{{ runtime ? 'No container to open a shell in.' : 'Loading containers.' }}</HText>
    <HText v-if="phase === 'closed' && closeReason" tone="muted">Session ended: {{ closeReason }}.</HText>

    <div v-show="phase !== 'idle'" ref="host" class="term" role="group" aria-label="Terminal" />

    <HText tone="muted" size="sm">Every session is recorded: who, which container, when.</HText>
  </HStack>
</template>

<style scoped>
.term {
  min-height: 26rem;
  max-width: 100%;
  padding: 0.5rem;
  overflow: hidden;
  background: #0b0e14;
  border: 1px solid var(--h-border);
  border-radius: var(--h-radius-control);
}
</style>
