<script setup lang="ts">
/**
 * The "Forwards to" cell: the address Traefik assembled, or WHY it could not.
 *
 * ONE COMPONENT, TWO PAGES. The deployment's Runtime tab and the host-wide Routing page render the
 * same fact from the same shape, and they used to render it from two copies of the same ternary —
 * which is how the two would drift the moment one of them learned something the other did not.
 *
 * WHY NOT A TERNARY. `target === null` had two branches, `port === null ? 'no port declared' :
 * 'not on the ingress network'`, and under swarm the second one was wrong for EVERY route: the
 * routers live on the SERVICE, whose address this node cannot see, so a perfectly healthy stack read
 * as a misconfiguration. The server now says which of the three it is (`targetReason`), and the three
 * are coloured by whether anyone should act:
 *
 *   no-port         amber   — the router works, Traefik is guessing the port; probably fix it
 *   not-on-ingress  red     — a real misconfiguration; the hostname 404s until it is fixed
 *   unknown-node    neutral — normal under swarm; nothing to do
 *
 * THE FINAL `v-else` IS THE OLD-SERVER BRANCH. `targetReason` is typed non-optional because the UI
 * and the server ship together, but a UI pointed at an older binary receives no field at all. That
 * must not fall into "not on the ingress network" — accusing a healthy stack of the exact bug this
 * replaced. It falls here instead, and says only what is true: nothing is known.
 */
import type { RuntimeRoute } from '../api/types';

defineProps<{ route: RuntimeRoute }>();
</script>

<template>
  <!-- `cell-clip`: an IPv6 address is long, the column is fixed-width, and the whole value stays
       reachable in `title`. The reasons below are short by construction and wrap on their own. -->
  <span v-if="route.target" class="mono cell-clip" :title="route.target">{{ route.target }}</span>
  <span v-else-if="route.targetReason === 'no-port'" class="s-leaked">
    no port declared
  </span>
  <span v-else-if="route.targetReason === 'not-on-ingress'" class="s-failed">
    not on the ingress network
  </span>
  <span
    v-else-if="route.targetReason === 'unknown-node'"
    class="s-unverifiable"
    title="A swarm task on another node. The manager lists it but cannot see its address — this is normal, and nothing needs fixing."
  >
    address not known from this node
  </span>
  <span v-else class="s-unverifiable" title="This server did not say why.">not known</span>
</template>
