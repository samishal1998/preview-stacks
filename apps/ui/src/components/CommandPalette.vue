<script setup lang="ts">
/**
 * ⌘K — go anywhere, by typing.
 *
 * WHY IT EXISTS. Navigation shortcuts (`g d`, `g j`) are fast once memorised and useless before
 * that, and neither they nor the sidebar can reach a *specific* deployment — the thing an operator
 * is almost always actually looking for. The palette is the one surface where "pr-31" is a
 * complete thought.
 *
 * DATA IS FETCHED WHEN IT OPENS, not held. This app already polls; a palette that kept its own
 * subscription would poll for a list nobody is looking at, and a list fetched at open cannot be
 * stale by the time it is read.
 *
 * SCORING IS SUBSEQUENCE, NOT SUBSTRING. `sdb` should find `shared-db` — a substring match cannot,
 * and an operator who has to remember the exact spelling is back to using the sidebar. Ties break
 * toward earlier and more contiguous matches, so an exact prefix always wins.
 */
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../api/client';
import type { DeploymentRow, DeploymentsResponse } from '../api/types';

const router = useRouter();
const open = ref(false);
const q = ref('');
const cursor = ref(0);
const deployments = ref<DeploymentRow[]>([]);
const input = ref<HTMLInputElement | null>(null);
const listEl = ref<HTMLElement | null>(null);

type Item = {
  id: string;
  label: string;
  hint?: string;
  group: string;
  to: string;
  /** Always-visible entries (the static pages) sort above matched data when the query is empty. */
  base: number;
};

const PAGES: Item[] = [
  { id: 'p.dash', label: 'Dashboard', hint: 'g h', group: 'Go to', to: '/', base: 9 },
  { id: 'p.dep', label: 'Deployments', hint: 'g d', group: 'Go to', to: '/deployments', base: 8 },
  { id: 'p.jobs', label: 'Jobs', hint: 'g j', group: 'Go to', to: '/jobs', base: 7 },
  { id: 'p.specs', label: 'Specs', group: 'Go to', to: '/specs', base: 6 },
  { id: 'p.routing', label: 'Routing', group: 'Go to', to: '/routing', base: 5 },
  { id: 'p.reg', label: 'Registries', group: 'Go to', to: '/registries', base: 4 },
  { id: 'p.notif', label: 'Notifiers', group: 'Go to', to: '/notifiers', base: 3 },
  { id: 'p.set', label: 'Settings', hint: 'g s', group: 'Go to', to: '/settings', base: 2 },
  { id: 'a.submit', label: 'Submit a spec', hint: 'g n', group: 'Actions', to: '/submit', base: 1 },
];

/**
 * Fuzzy subsequence score, or null for no match.
 *
 * Higher is better. A run of adjacent matched characters scores far above scattered ones, and a
 * match at position 0 gets a bonus — together those make an exact prefix beat a lucky scatter,
 * which is the only ranking property a user actually notices.
 */
function score(text: string, query: string): number | null {
  if (!query) return 0;
  const t = text.toLowerCase();
  const query_ = query.toLowerCase();
  let ti = 0;
  let points = 0;
  let run = 0;
  for (const ch of query_) {
    const at = t.indexOf(ch, ti);
    if (at === -1) return null;
    run = at === ti && ti > 0 ? run + 1 : 0;
    points += 10 + run * 12 - Math.min(at - ti, 8);
    if (at === 0) points += 25;
    ti = at + 1;
  }
  // Shorter haystacks win ties: "pr-7" should beat "shopfront-pr-7x" for the query "pr7".
  return points - t.length * 0.4;
}

const items = computed<Item[]>(() => {
  const fromDeployments: Item[] = deployments.value.map((d) => ({
    id: `d.${d.id}`,
    label: d.id,
    hint: d.stack ?? undefined,
    group: 'Deployments',
    to: `/deployments/${encodeURIComponent(d.id)}/overview`,
    base: 0,
  }));
  const all = [...PAGES, ...fromDeployments];
  const query = q.value.trim();
  if (!query) return all.slice().sort((a, b) => b.base - a.base);
  return all
    .map((it) => ({ it, s: score(`${it.label} ${it.hint ?? ''}`, query) }))
    .filter((r): r is { it: Item; s: number } => r.s !== null)
    .sort((a, b) => b.s - a.s || b.it.base - a.it.base)
    .map((r) => r.it);
});

/** Group headers are rendered by comparing with the previous row — no second grouped structure. */
const rows = computed(() =>
  items.value.map((it, i) => ({ it, head: i === 0 || items.value[i - 1]!.group !== it.group })),
);

async function show(): Promise<void> {
  open.value = true;
  q.value = '';
  cursor.value = 0;
  await nextTick();
  input.value?.focus();
  const r = await api.get<DeploymentsResponse>('/api/deployments');
  // A palette that cannot list deployments still navigates pages — failing closed here would take
  // the whole surface away over a transient error.
  if (r.ok) deployments.value = r.body.deployments;
}

function hide(): void {
  open.value = false;
}

function choose(it: Item | undefined): void {
  if (!it) return;
  hide();
  void router.push(it.to);
}

watch(cursor, async () => {
  await nextTick();
  listEl.value?.querySelector('[data-on="true"]')?.scrollIntoView({ block: 'nearest' });
});
// A new query invalidates the old position: leaving the cursor on row 4 of a list that just became
// two rows long is how a palette navigates somewhere nobody asked for.
watch(q, () => (cursor.value = 0));

function onKey(e: KeyboardEvent): void {
  // The one binding that must work from ANYWHERE, including from inside a text field — a palette
  // you cannot open while your cursor sits in a search box is a palette you reach for and miss.
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault();
    if (open.value) hide();
    else void show();
    return;
  }
  if (!open.value) return;
  if (e.key === 'Escape') {
    e.preventDefault();
    hide();
  } else if (e.key === 'ArrowDown' || (e.key === 'n' && e.ctrlKey)) {
    e.preventDefault();
    cursor.value = Math.min(cursor.value + 1, items.value.length - 1);
  } else if (e.key === 'ArrowUp' || (e.key === 'p' && e.ctrlKey)) {
    e.preventDefault();
    cursor.value = Math.max(cursor.value - 1, 0);
  } else if (e.key === 'Enter') {
    e.preventDefault();
    choose(items.value[cursor.value]);
  }
}

onMounted(() => document.addEventListener('keydown', onKey));
onUnmounted(() => document.removeEventListener('keydown', onKey));

defineExpose({ show });
</script>

<template>
  <Transition name="fade">
    <div v-if="open" class="scrim palette-scrim" @click.self="hide">
      <div class="palette" role="dialog" aria-modal="true" aria-label="Command palette">
        <div class="palette-q">
          <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
            <circle cx="11" cy="11" r="7" fill="none" stroke="currentColor" stroke-width="2" />
            <path d="M16.5 16.5 21 21" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
          </svg>
          <input
            ref="input"
            v-model="q"
            type="text"
            placeholder="Go to a deployment, a page, an action…"
            aria-label="Search"
            autocomplete="off"
            spellcheck="false"
          />
          <kbd>esc</kbd>
        </div>

        <div ref="listEl" class="palette-list">
          <p v-if="!rows.length" class="palette-empty">
            Nothing matches “{{ q }}”.
          </p>
          <template v-for="(r, i) in rows" :key="r.it.id">
            <div v-if="r.head" class="palette-group">{{ r.it.group }}</div>
            <button
              class="palette-row"
              :data-on="i === cursor"
              @click="choose(r.it)"
              @mousemove="cursor = i"
            >
              <span class="palette-label">{{ r.it.label }}</span>
              <span v-if="r.it.hint" class="palette-hint">{{ r.it.hint }}</span>
            </button>
          </template>
        </div>

        <div class="palette-foot">
          <span><kbd>↑</kbd><kbd>↓</kbd> move</span>
          <span><kbd>↵</kbd> open</span>
          <span class="grow" />
          <span>{{ items.length }} result{{ items.length === 1 ? '' : 's' }}</span>
        </div>
      </div>
    </div>
  </Transition>
</template>
