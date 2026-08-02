<script setup lang="ts">
/**
 * "Show equivalent command" — the GCP pattern.
 *
 * WHY THIS EXISTS RATHER THAN A SNIPPET ON THE PAGE. A `curl` line printed next to a form is read by
 * everyone and needed by almost nobody: it is permanent clutter charged to every operator so that
 * the occasional scripter does not have to look something up. Behind a button it costs one click
 * when you want it and nothing when you do not — and, unlike a static snippet in the docs, it is
 * generated from what is actually in the form right now, so it cannot drift from the request the
 * button next to it would send.
 *
 * IT IS NOT A SECOND SOURCE OF TRUTH. The caller passes the same values it is about to POST. If the
 * form changes, this changes with it, because there is nothing here to keep in sync by hand.
 *
 * THE TOKEN IS NEVER INTERPOLATED. `$PSTACK_TOKEN` stays a shell variable: a rendered command lands
 * in screenshots, pasted messages and shell history, and a control-plane token in any of those is
 * the whole host. The panel says so rather than silently omitting it.
 */
import { computed, ref } from 'vue';
import { toast } from '../composables/useToasts';

const props = defineProps<{
  /** Shown as the button label's object, e.g. "this registration". */
  what: string;
  /** The HTTP request the adjacent action performs. */
  method: string;
  /** Path only — the host is filled in from where the UI is served. */
  path: string;
  body?: unknown;
  /** The `pstack` invocation that does the same thing, when one exists. */
  cli?: string;
}>();

const open = ref(false);
const tab = ref<'curl' | 'cli'>('curl');

const base = computed(() => `${location.protocol}//${location.host}`);

const curl = computed(() => {
  const lines = [`curl -X ${props.method} ${base.value}${props.path} \\`];
  lines.push(`  -H 'Authorization: Bearer $PSTACK_TOKEN' \\`);
  if (props.body !== undefined) {
    lines.push(`  -H 'Content-Type: application/json' \\`);
    // Single-quoted so nothing in a value is expanded by the shell; embedded quotes are escaped
    // the only way that works inside single quotes.
    const json = JSON.stringify(props.body, null, 2).split("'").join(`'\\''`);
    lines.push(`  -d '${json}'`);
  } else {
    lines[lines.length - 1] = `  -H 'Authorization: Bearer $PSTACK_TOKEN'`;
  }
  return lines.join('\n');
});

const text = computed(() => (tab.value === 'curl' ? curl.value : (props.cli ?? '')));

async function copy(): Promise<void> {
  try {
    await navigator.clipboard.writeText(text.value);
    toast('ok', 'Copied.');
  } catch {
    toast('error', 'Could not copy — your browser blocked clipboard access.');
  }
}
</script>

<template>
  <div class="eqc">
    <button class="ghost sm" :aria-expanded="open" @click="open = !open">
      <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="m7 8-4 4 4 4" /><path d="m17 8 4 4-4 4" /><path d="M14 4 10 20" />
      </svg>
      {{ open ? 'Hide' : 'Show' }} equivalent command
    </button>

    <Transition name="fade">
      <div v-if="open" class="eqc-panel">
        <div class="eqc-tabs" role="tablist">
          <button
            class="ghost sm"
            role="tab"
            :aria-selected="tab === 'curl'"
            :data-on="tab === 'curl'"
            @click="tab = 'curl'"
          >
            curl
          </button>
          <button
            v-if="cli"
            class="ghost sm"
            role="tab"
            :aria-selected="tab === 'cli'"
            :data-on="tab === 'cli'"
            @click="tab = 'cli'"
          >
            pstack
          </button>
          <span class="grow" />
          <button class="ghost sm" @click="copy">Copy</button>
        </div>

        <pre class="eqc-code">{{ text }}</pre>

        <p class="eqc-note">
          Runs {{ what }} exactly as the button above does.
          <b>$PSTACK_TOKEN</b> is left as a shell variable on purpose — a real token in a screenshot
          or a shell history is the whole host.
        </p>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.eqc {
  min-width: 0;
}
.eqc-panel {
  margin-top: var(--s3);
  border: 1px solid var(--line);
  border-radius: var(--r2);
  background: var(--sunk);
  overflow: hidden;
}
.eqc-tabs {
  display: flex;
  align-items: center;
  gap: var(--s1);
  padding: var(--s2);
  border-bottom: 1px solid var(--line-soft);
}
.eqc-tabs [data-on='true'] {
  background: var(--accent-soft);
  color: var(--accent);
}
.eqc-code {
  margin: 0;
  padding: var(--s3);
  font: var(--t-sm) / 1.6 var(--mono);
  white-space: pre;
  overflow-x: auto;
  color: var(--fg-dim);
}
.eqc-note {
  padding: 0 var(--s3) var(--s3);
  font-size: var(--t-xs);
  color: var(--fg-mute);
}
</style>
