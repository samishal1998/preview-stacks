/**
 * `GET /api/signals` and the three node routes, against the real binary and a fake `docker` on PATH.
 *
 * What the feature is for: something outside pstack adds and removes worker machines, and needs two
 * answers — is a task stuck for want of room, and is a worker running nothing. These tests drive
 * exactly that loop: read the signals, drain the empty worker, refuse to remove it while it is up,
 * then remove it once docker says the machine has gone.
 *
 * The shim is SIGNALS_SHIM, NOT an extension of SWARM_SHIM: a shell `case` takes the first arm that
 * matches, and other suites already answer `node inspect` and `node ls` through SWARM_SHIM. Adding
 * arms there would change what those suites see.
 */
import { describe, expect, test } from 'bun:test';
import { bootServer, until } from '../harness/server.ts';
import { dockerShim } from '../harness/docker-shim.ts';
import { eventTap } from '../harness/receiver.ts';
import { NO_SWARM_SHIM } from '../gen/goldens.table.ts';

type Node = {
  id: string;
  hostname: string;
  role: string;
  state: string;
  availability: string;
  tasks: number;
  emptySince: number | null;
};
type Signals = { v: number; swarm: boolean; reachable: boolean; at: number; nodes: Node[]; stuck: Array<{ task: string; service: string; stack: string; reason: string }> };

/** One manager and two workers; worker-1 carries a task, worker-2 carries only a global one. */
const nodes = (wrk2 = { status: 'Ready', availability: 'Active' }) =>
  `  "node ls --format {{json .}}") printf '%s\\n' ` +
  `'{"ID":"n1","Hostname":"mgr","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}' ` +
  `'{"ID":"n2","Hostname":"wrk1","Status":"Ready","Availability":"Active","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}' ` +
  `'{"ID":"n3","Hostname":"wrk2","Status":"${wrk2.status}","Availability":"${wrk2.availability}","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}' ;;`;

const SIGNALS_SHIM = (opts: { stuck?: boolean; wrk2?: { status: string; availability: string } } = {}) => {
  const { stuck = true, wrk2 = { status: 'Ready', availability: 'Active' } } = opts;
  // The Error value is wrapped in quotes by docker itself, and `--no-trunc` is what keeps the
  // sentence whole — both are what the parser depends on, so the fixture spells them out.
  const pending = stuck
    ? ` '{"ID":"t2","Name":"pr-42_api.1","Node":"","DesiredState":"Running","CurrentState":"Pending 2 minutes ago","Error":"\\"no suitable node (insufficient resources on 2 nodes)\\""}'`
    : '';
  return [
    `  "info --format {{json .Swarm}}") printf '%s\\n' '{"NodeID":"n1","NodeAddr":"10.0.0.1","LocalNodeState":"active","ControlAvailable":true,"RemoteManagers":[{"NodeID":"n1","Addr":"10.0.0.1:2377"}]}' ;;`,
    nodes(wrk2),
    `  "service ls --format {{json .}}") printf '%s\\n' '{"ID":"s1","Name":"pr-42_web","Mode":"replicated","Replicas":"1/1"}' '{"ID":"s2","Name":"pr-42_api","Mode":"replicated","Replicas":"0/1"}' '{"ID":"s3","Name":"logs","Mode":"global","Replicas":"3/3"}' ;;`,
    `  "service ps --no-trunc --filter desired-state=running --format {{json .}} s1 s2 s3") printf '%s\\n' ` +
      `'{"ID":"t1","Name":"pr-42_web.1","Node":"wrk1","DesiredState":"Running","CurrentState":"Running 4 minutes ago","Error":""}'` +
      `${pending} ` +
      `'{"ID":"t3","Name":"logs.n3","Node":"wrk2","DesiredState":"Running","CurrentState":"Running 6 hours ago","Error":""}' ;;`,
    `  "node update --availability drain n3") printf '%s\\n' 'n3' ;;`,
    `  "node update --availability active n3") printf '%s\\n' 'n3' ;;`,
    `  "node rm n3") printf '%s\\n' 'n3' ;;`,
  ].join('\n');
};

const read = async (base: string, H: Record<string, string>) =>
  (await (await fetch(`${base}/api/signals`, { headers: H })).json()) as Signals;

describe('API: node signals', () => {
  test('a one-machine host has nothing to report', async () => {
    // negative control: answer `swarm: true` whenever docker replies — a compose host would claim a
    // cluster, and whatever reads this would start looking for machines to add.
    const docker = dockerShim(NO_SWARM_SHIM);
    const s = await bootServer({ tag: 'signals-compose', pathPrefix: docker.dir });
    try {
      const body = await read(s.base, s.H);
      expect(body.swarm).toBe(false);
      expect(body.nodes).toEqual([]);
      expect(body.stuck).toEqual([]);
      // Every list is present and empty, never null: a consumer calls .map on them unguarded.
      expect(body).toHaveProperty('nodes');
      expect(body).toHaveProperty('stuck');
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);

  test('the nodes, their task counts, and the task docker would not place', async () => {
    // negative control: count a global service's tasks — wrk2 reads 1 task, never looks empty, and
    // no machine is ever returned.
    const docker = dockerShim(SIGNALS_SHIM());
    const s = await bootServer({ tag: 'signals-view', pathPrefix: docker.dir });
    try {
      const body = await read(s.base, s.H);
      expect(body.swarm).toBe(true);
      expect(body.nodes.map((n) => [n.hostname, n.role, n.tasks])).toEqual([
        ['mgr', 'manager', 0],
        ['wrk1', 'worker', 1],
        ['wrk2', 'worker', 0],
      ]);
      // The manager is never offered as a machine to take away, however idle it looks.
      expect(body.nodes.find((n) => n.hostname === 'mgr')!.emptySince).toBeNull();
      expect(body.nodes.find((n) => n.hostname === 'wrk1')!.emptySince).toBeNull();
      expect(typeof body.nodes.find((n) => n.hostname === 'wrk2')!.emptySince).toBe('number');

      // negative control: drop `--no-trunc` from the docker command — the reason arrives cut at 30
      // characters (`no suitable node (insufficie…`) and this comparison fails.
      expect(body.stuck).toEqual([
        {
          task: 't2',
          service: 'pr-42_api',
          stack: 'pr-42',
          reason: 'no suitable node (insufficient resources on 2 nodes)',
        },
      ]);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);

  test('drain, then refuse to remove until the machine is really gone', async () => {
    // negative control: remove a node whose docker state is not `down` — the first DELETE succeeds,
    // and a node that is merely unreachable for a moment loses everything it was running.
    const docker = dockerShim(SIGNALS_SHIM(), { record: true });
    const s = await bootServer({ tag: 'signals-drain', pathPrefix: docker.dir });
    const { base, H } = s;
    try {
      const drain = await fetch(`${base}/api/swarm/nodes/n3/drain`, { method: 'POST', headers: H });
      expect(drain.status).toBe(200);
      expect(await drain.json()).toEqual({ node: 'n3', hostname: 'wrk2', availability: 'drain' });
      // The shim records what it was called with: no `docker`, no quotes.
      expect(docker.calls()).toContain('node update --availability drain n3');

      const tooSoon = await fetch(`${base}/api/swarm/nodes/n3`, { method: 'DELETE', headers: H });
      expect(tooSoon.status).toBe(409);
      expect(docker.calls()).not.toContain('node rm n3');

      // The machine is gone: docker now reports the node down and drained.
      docker.rewrite(SIGNALS_SHIM({ wrk2: { status: 'Down', availability: 'Drain' } }));
      const gone = await fetch(`${base}/api/swarm/nodes/n3`, { method: 'DELETE', headers: H });
      expect(gone.status).toBe(200);
      expect(docker.calls()).toContain('node rm n3');
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 20_000);

  test('a stuck task reaches a notifier once, and clears when it is placed', async () => {
    // negative control: emit on every tick instead of on the change — the tap collects the same
    // raise again and the count below is no longer 1.
    const docker = dockerShim(SIGNALS_SHIM());
    const s = await bootServer({ tag: 'signals-events', pathPrefix: docker.dir, env: { PSTACK_SIGNALS_TICK_MS: '150' } });
    const { base, H } = s;
    try {
      const tap = await eventTap(base, H);
      const of = (name: string) => tap.got.filter((d) => d.event === name).map((d) => (d.body!.data as { id: string }).id);

      await until(async () => of('signal.raised'), (ids) => ids.includes('stuck/pr-42_api'), 10_000);
      await until(async () => of('signal.raised'), (ids) => ids.includes('empty/n3'), 10_000);
      const raisedOnce = of('signal.raised').filter((id) => id === 'stuck/pr-42_api');
      expect(raisedOnce.length).toBe(1);

      // Everything placed: the stuck signal clears, and nothing else is claimed to have changed.
      docker.rewrite(SIGNALS_SHIM({ stuck: false }));
      await until(async () => of('signal.cleared'), (ids) => ids.includes('stuck/pr-42_api'), 10_000);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 30_000);
});
