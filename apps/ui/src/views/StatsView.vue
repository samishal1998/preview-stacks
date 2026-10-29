<script setup lang="ts">
/**
 * Node pressure: per node, disk, memory and load as bars against the pressure settings, an hour of
 * load per core, the pressure the server reports and the last prune; then what each stack uses. Polls every
 * 10 s.
 *
 * Pressure is the server's verdict (`node.pressure`), never recomputed here. A null reading (a stale
 * node) renders as unknown text, never as an empty bar: an empty bar reads as 0. The upgrade advice
 * shows only when no node has ever reported (NP-R13); a node that reported once and went quiet is
 * Stale, because that is the incident window and the host is already upgraded.
 *
 * Bars are plain CSS with their value as visible text beside them, no ARIA meter (NP-R25). The
 * thresholds come from `GET /api/settings`; a setting of 0 is off and draws no tick.
 */
import { computed, ref } from 'vue';
import { api, problem } from '../api/client';
import type { HostSettings, NodeStats, SettingKey, Stats } from '../api/types';
import { usePolling } from '../composables/usePolling';
import { sentence } from '../composables/useFormat';
import { state } from '../composables/useControlPlane';
import { deploymentFor, diskUsedPct, fmtBytes, historyPerCore, loadPerCore, memFreePct, neverReported, pressureLabel, sortStacks } from '../composables/stats';
import ErrorNote from '../components/ErrorNote.vue';
import RefreshButton from '../components/RefreshButton.vue';
import RelativeTime from '../components/RelativeTime.vue';
import SkeletonList from '../components/SkeletonList.vue';

const stats = ref<Stats | null>(null);
const limits = ref<Partial<Record<SettingKey, number>>>({});
const error = ref('');
const loaded = ref(false);

async function load(): Promise<void> {
  const [s, t] = await Promise.all([api.get<Stats>('/api/stats'), api.get<HostSettings>('/api/settings')]);
  loaded.value = true;
  if (t.ok) limits.value = Object.fromEntries((t.body.settings ?? []).filter((r) => typeof r.value === 'number').map((r) => [r.key, r.value]));
  if (!s.ok) {
    // A failed background poll keeps the last good page; only the first read shows the error.
    if (!stats.value) error.value = problem(s, 'read stats');
    return;
  }
  error.value = '';
  stats.value = { ...s.body, nodes: s.body.nodes ?? [], stacks: s.body.stacks ?? [] };
}
usePolling(load, 10_000);

const none = computed(() => !!stats.value && neverReported(stats.value));

type Bar = { key: string; label: string; pct: number | null; value: string; tick: number | null; hot: boolean; detail: string };
const clamp = (v: number) => Math.min(100, Math.max(0, v));
/** The threshold, or null for no tick: 0 is off, and an unread setting is unknown, not off. */
const limit = (k: SettingKey): number | null => limits.value[k] || null;
/** 'Limit 90%', 'Limit off', or '' when the settings could not be read. */
const limitText = (k: SettingKey, show: (v: number) => string): string => {
  const v = limits.value[k];
  return v === undefined ? '' : v === 0 ? 'Limit off' : `Limit ${show(v)}`;
};
/** The reading's own detail only when it is known; a stale node's line is just its limit. */
const detail = (known: boolean, reading: string, lim: string) => [known ? reading : '', lim].filter(Boolean).join(' · ');

function bars(n: NodeStats): Bar[] {
  const disk = diskUsedPct(n);
  const memFree = memFreePct(n);
  const load = loadPerCore(n);
  const diskLimit = limit('pressure_disk_pct');
  const memLimit = limit('pressure_mem_pct');
  const cpuLimit = limit('pressure_cpu_load');
  // Load has no 100: scale so the threshold sits at 80% of the track, or the load itself if higher.
  const cpuScale = Math.max((cpuLimit ?? 0) * 1.25, load ?? 0, 1);
  return [
    {
      key: 'disk',
      label: 'Disk used',
      pct: disk,
      value: disk === null ? 'Unknown' : `${Math.round(disk)}%`,
      tick: diskLimit,
      hot: n.pressure.includes('disk'),
      detail: detail(disk !== null, `${fmtBytes(n.diskFree)} free of ${fmtBytes(n.diskTotal)}`, limitText('pressure_disk_pct', (v) => `${v}%`)),
    },
    {
      key: 'memory',
      label: 'Memory used',
      pct: memFree === null ? null : 100 - memFree,
      value: memFree === null ? 'Unknown' : `${Math.round(100 - memFree)}%`,
      tick: memLimit === null ? null : 100 - memLimit,
      hot: n.pressure.includes('memory'),
      detail: detail(memFree !== null, `${fmtBytes(n.memAvailable)} free of ${fmtBytes(n.memTotal)}`, limitText('pressure_mem_pct', (v) => `${v}% free`)),
    },
    {
      key: 'cpu',
      label: 'Load per core',
      pct: load === null ? null : (load / cpuScale) * 100,
      value: load === null ? 'Unknown' : load.toFixed(2),
      tick: cpuLimit === null ? null : (cpuLimit / cpuScale) * 100,
      hot: n.pressure.includes('cpu'),
      detail: detail(n.cores !== null, `${n.cores} cores`, limitText('pressure_cpu_load', String)),
    },
  ];
}

/** A stale node's `at` is null; its newest sample still says when it last reported. */
const lastSeen = (n: NodeStats) => n.at ?? n.history.at(-1)?.at ?? null;
/** Per core, the scale the cpu limit is on; a raw peak on an 8-core node reads as over a per-core limit. */
const peak = (n: NodeStats): number | null => {
  const h = historyPerCore(n);
  return h ? Math.max(...h) : null;
};
/** The shape only; without cores it is raw load5, drawn but never given a number. */
const spark = (n: NodeStats) => {
  const h = historyPerCore(n) ?? n.history.map((x) => x.load5);
  const top = Math.max(...h) || 1;
  return h.map((v) => clamp((v / top) * 100));
};

const stacks = computed(() =>
  sortStacks(stats.value?.stacks ?? []).map((s) => ({ ...s, deployment: deploymentFor(s.stack, state.deployments) })),
);
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1>Stats</h1>
        <div class="sub">Node pressure and what each stack uses</div>
      </div>
      <span class="grow" />
      <RefreshButton :run="load" />
    </div>

    <ErrorNote v-if="error" :text="error" title="Could not read stats." />

    <SkeletonList v-if="!loaded && !error" :rows="4" />

    <section v-else-if="stats && none" class="panel">
      <div class="banner plain">
        <b>No agent reports.</b>
        <p>Run <code>pstack upgrade</code>.</p>
      </div>
    </section>

    <template v-else-if="stats">
      <section class="panel">
        <div class="phead">
          <h2 class="section">Nodes</h2>
        </div>
        <div class="nodes">
          <article v-for="n in stats.nodes" :key="n.node ?? n.hostname" class="node">
            <div class="node-head">
              <h3 class="mono">{{ n.hostname }}</h3>
              <span v-if="n.stale" class="badge unknown">Stale</span>
              <template v-else-if="n.pressure.length">
                <span v-for="k in n.pressure" :key="k" class="badge fail"><span class="dot" />{{ pressureLabel(k) }}</span>
              </template>
              <span v-else class="badge ok">OK</span>
            </div>
            <div class="mute small">
              <template v-if="n.role">{{ sentence(n.role) }} · </template>
              <template v-if="lastSeen(n)">Reported <RelativeTime :at="lastSeen(n)" /></template>
              <template v-else>Never reported</template>
            </div>

            <div v-for="b in bars(n)" :key="b.key" class="meter">
              <div class="meter-line">
                <span>{{ b.label }}</span>
                <span class="mono" :class="{ bad: b.hot, mute: b.pct === null }">{{ b.value }}</span>
              </div>
              <div v-if="b.pct !== null" class="track" aria-hidden="true">
                <div class="fill" :class="{ hot: b.hot }" :style="{ width: `${clamp(b.pct)}%` }" />
                <div v-if="b.tick !== null" class="tick" :style="{ left: `${clamp(b.tick)}%` }" />
              </div>
              <div v-if="b.detail" class="mute small">{{ b.detail }}</div>
            </div>

            <div v-if="n.history.length" class="meter">
              <div class="meter-line">
                <span>{{ peak(n) === null ? 'Load, last hour' : 'Load per core, last hour' }}</span>
                <span v-if="peak(n) !== null" class="mono">peak {{ peak(n)!.toFixed(2) }}</span>
              </div>
              <div class="spark" aria-hidden="true">
                <span v-for="(h, i) in spark(n)" :key="i" :style="{ height: `${Math.max(h, 4)}%` }" />
              </div>
            </div>

            <div class="mute small">
              <template v-if="n.prune">
                Pruned <RelativeTime :at="n.prune.at" /> · {{ fmtBytes(n.prune.freedBytes) }} freed · {{ sentence(n.prune.reason) }}
              </template>
              <template v-else>Not pruned yet</template>
            </div>
          </article>
        </div>
      </section>

      <section class="panel">
        <div class="phead">
          <h2 class="section">Stacks</h2>
        </div>
        <div v-if="stacks.length" class="table-scroll">
          <table role="table" class="cards">
            <thead role="rowgroup">
              <tr role="row">
                <th role="columnheader">Stack</th>
                <th role="columnheader" class="num">Memory</th>
                <th role="columnheader" class="num">CPU</th>
                <th role="columnheader" class="num">Containers</th>
                <th role="columnheader">Nodes</th>
              </tr>
            </thead>
            <tbody role="rowgroup">
              <tr v-for="s in stacks" :key="s.stack" role="row">
                <td role="cell" class="name" data-label="stack">
                  <RouterLink v-if="s.deployment" :to="`/deployments/${encodeURIComponent(s.deployment)}`">{{ s.stack }}</RouterLink>
                  <template v-else>{{ s.stack }}</template>
                </td>
                <td role="cell" class="num mono" data-label="memory">{{ fmtBytes(s.mem) }}</td>
                <td role="cell" class="num mono" data-label="cpu">{{ s.cpuPct.toFixed(1) }}%</td>
                <td role="cell" class="num mono" data-label="containers">{{ s.containers }}</td>
                <td role="cell" class="mono" data-label="nodes">{{ s.nodes.join(', ') || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="mute">No stack containers reported.</p>
      </section>
    </template>
  </div>
</template>

<style scoped>
.nodes {
  display: grid;
  gap: var(--s4);
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 18rem), 1fr));
}
.node {
  display: grid;
  gap: var(--s3);
  align-content: start;
  padding: var(--s3) var(--s4);
  border: 1px solid var(--line);
  border-radius: var(--r2);
  background: var(--panel-2);
  min-width: 0;
}
.node-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--s2);
}
.node-head h3 {
  margin: 0;
  margin-inline-end: auto;
  font-size: var(--t-sm);
  overflow-wrap: anywhere;
}
.small {
  font-size: var(--t-xs);
}
.meter {
  display: grid;
  gap: var(--s1);
}
.meter-line {
  display: flex;
  justify-content: space-between;
  gap: var(--s2);
  font-size: var(--t-sm);
}
.track {
  position: relative;
  height: var(--s2);
  border-radius: var(--r-full);
  background: var(--sunk);
  box-shadow: inset 0 0 0 1px var(--line);
}
.fill {
  height: 100%;
  border-radius: var(--r-full);
  background: var(--accent);
}
.fill.hot {
  background: var(--fail);
}
/* The threshold: a mark across the track, taller than it so it reads over the fill. */
.tick {
  position: absolute;
  top: calc(var(--s1) * -1);
  bottom: calc(var(--s1) * -1);
  width: 2px;
  margin-inline-start: -1px;
  background: var(--fg-dim);
}
.spark {
  display: flex;
  align-items: flex-end;
  height: var(--s6);
  gap: 1px;
}
.spark > span {
  flex: 1;
  min-width: 0;
  background: color-mix(in srgb, var(--accent) 70%, transparent);
  border-radius: 1px 1px 0 0;
}
th.num,
td.num {
  text-align: right;
}
</style>
