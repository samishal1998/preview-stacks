/**
 * `GET /api/stats`, against the real binary and a fake `docker` on PATH.
 *
 * The stats loop reads `docker info` and the agent's logs on a timer (PSTACK_STATS_TICK_MS); the
 * route only reads what the last tick recorded. So each test sets a fast tick and polls.
 *
 * The shim is statsShim, NOT an extension of SWARM_SHIM: other suites already answer `info` through
 * SWARM_SHIM, and a shell `case` takes the first arm that matches. The agent's output is the agent
 * script's own fixture, with its node id blanked, as a compose host's agent prints it.
 *
 * The "no agent" server gets a docker whose every arm exits 1 (NP-R14), never the real docker on
 * PATH: that would read a developer's or a CI runner's own daemon, and its own pstack-agent.
 */
import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { bootServer, until, waitJob } from '../harness/server.ts';
import { dockerShim, sq } from '../harness/docker-shim.ts';
import { REPO } from '../harness/impl.ts';

type Stats = {
  v: number;
  at: number;
  agent: boolean;
  nodes: Array<{
    node: string | null;
    hostname: string;
    role: string | null;
    stale: boolean;
    memAvailable: number | null;
    pressure: string[];
  }>;
  stacks: Array<{ stack: string; cpuPct: number; mem: number; containers: number; nodes: string[] }>;
};

const AGENT_OUTPUT = readFileSync(join(REPO, 'packages/pstack/templates/agent/testdata/agent-output.txt'), 'utf8')
  .replaceAll('"node":"x8k2m9qv1n3p4r5s6t7u8w9y0"', '"node":""')
  .trimEnd();

/**
 * A compose host whose local agent has printed one batch. `$*` never carries the `2>/dev/null`. The
 * fixture's disk is 95 % used, so the default batch is in disk pressure.
 */
const statsShim = (agent = AGENT_OUTPUT) =>
  [
    `  "info --format {{json .Swarm}}") printf '%s\\n' '{"LocalNodeState":"inactive","NodeID":"","ControlAvailable":false}' ;;`,
    `  "logs --since 90s pstack-agent") printf '%s\\n' ${sq(agent)} ;;`,
  ].join('\n');

/** A docker that answers nothing. First, so it wins over the shim's default `exit 0`. */
const SILENT_SHIM = '  *) exit 1 ;;';

const read = async (base: string, H: Record<string, string>) =>
  (await (await fetch(`${base}/api/stats`, { headers: H })).json()) as Stats;

describe('API: node stats', () => {
  test('a compose host reports its one node and the stacks on it', async () => {
    // negative control: build the response from SwarmInfo's node list on a compose host too → there
    // is none, nodes stays [], and the poll below times out.
    const docker = dockerShim(statsShim());
    const s = await bootServer({ tag: 'stats-compose', pathPrefix: docker.dir, env: { PSTACK_STATS_TICK_MS: '100' } });
    try {
      const body = await until(() => read(s.base, s.H), (b) => b.nodes.length === 1, 10_000);
      expect(body.v).toBe(1);
      expect(body.agent).toBe(true);
      expect(body.nodes.map((n) => [n.node, n.hostname, n.role, n.stale, n.memAvailable])).toEqual([
        [null, 'worker-1', null, false, 4294967296],
      ]);
      // negative control: keep the container in no stack → a third row named "".
      expect(body.stacks).toEqual([
        { stack: 'pr-7', cpuPct: 0.52, mem: 1610612736, containers: 1, nodes: ['worker-1'] },
        { stack: 'pr-42', cpuPct: 3.1, mem: 129394278, containers: 1, nodes: ['worker-1'] },
      ]);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);

  test('a node in pressure refuses an up until it recovers', async () => {
    // negative control: drop the capacity guard from startLifecycle → the first up succeeds.
    const docker = dockerShim(statsShim());
    const s = await bootServer({ tag: 'stats-capacity', pathPrefix: docker.dir, env: { PSTACK_STATS_TICK_MS: '100' } });
    const pressure = async () => (await read(s.base, s.H)).nodes[0]?.pressure;
    const up = async () => {
      const r = await fetch(`${s.base}/api/deployments/d/up`, { method: 'POST', headers: s.H });
      const { job } = (await r.json()) as { job: { id: string } };
      return (await waitJob(s, job.id)) as { state: string; outcome?: { steps: Array<{ phase: string; message?: string }> } };
    };
    try {
      const put = await fetch(`${s.base}/api/deployments/d`, {
        method: 'PUT',
        headers: s.H,
        body: JSON.stringify({ spec: 'version: 1\nstack: cap\naxes:\n  - name: a\n    up: "true"\n' }),
      });
      expect(put.ok).toBe(true);
      expect(await until(pressure, (p) => p?.join() === 'disk', 10_000)).toEqual(['disk']);
      const refused = await up();
      expect(refused.state).toBe('failed');
      expect(refused.outcome?.steps.map((x) => [x.phase, x.message])).toEqual([['capacity', 'worker-1: disk 95% ≥ 90%']]);

      const off = await fetch(`${s.base}/api/settings/pressure_disk_pct`, {
        method: 'PUT',
        headers: s.H,
        body: JSON.stringify({ value: 0 }),
      });
      expect(off.ok).toBe(true);
      expect(await until(pressure, (p) => p?.length === 0, 10_000)).toEqual([]);
      const allowed = await up();
      expect(allowed.outcome?.steps.map((x) => x.phase)).not.toContain('capacity');
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 30_000);

  test('no agent and no docker: agent false, every list empty', async () => {
    // negative control: report `agent: true` whenever the loop ran → this host claims a reporter it
    // does not have.
    const docker = dockerShim(SILENT_SHIM);
    const s = await bootServer({ tag: 'stats-silent', pathPrefix: docker.dir, env: { PSTACK_STATS_TICK_MS: '100' } });
    try {
      await Bun.sleep(400); // a few ticks
      const body = await read(s.base, s.H);
      expect(body.v).toBe(1);
      expect(body.agent).toBe(false);
      expect(body.nodes).toEqual([]);
      expect(body.stacks).toEqual([]);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);
});
