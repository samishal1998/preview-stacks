/**
 * Loki logging at deploy time, over HTTP.
 *
 * Logging is on when the control stack runs a `loki` container carrying the push-url label; the
 * docker shim plays that container (LOKI_SHIM, shared with the goldens). What only a real process
 * shows: the derived compose file a deploy hands docker, read back from disk; the plugin check that
 * stops a compose deploy before `up`; and the swarm job log naming the node without the plugin.
 */
import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { bootServer, waitJob, type Booted } from '../harness/server.ts';
import { arm, dockerShim } from '../harness/docker-shim.ts';
import { LOKI_PASSWORD, LOKI_SHIM, NODE_PLUGINS, SWARM_SHIM } from '../gen/goldens.table.ts';

type Job = {
  state: string;
  outcome?: { steps: Array<{ phase: string; message?: string }> };
  log?: Array<{ level: string; message: string }>;
};

/** A service with no `logging:` beside one with json-file, which must come through untouched. */
const COMPOSE = 'services:\n  web:\n    image: nginx\n  db:\n    image: postgres\n    logging:\n      driver: json-file\n';

/** The injected block in its fixed key order. Every option is a string: the driver reads nothing else. */
const block = (stack: string, service: string) => ({
  driver: 'loki',
  options: {
    'loki-url': `https://pstack:${LOKI_PASSWORD}@loki.preview.example.com/loki/api/v1/push`,
    'loki-external-labels': `service_name=${stack}-${service}`,
    'loki-relabel-config': '[{action: labeldrop, regex: filename}]',
    'loki-retries': '2',
    'loki-timeout': '1s',
    'loki-max-backoff': '800ms',
    mode: 'non-blocking',
    'keep-file': 'false',
    'max-size': '10m',
    'max-file': '3',
  },
});

describe('Loki logging: injection at deploy', () => {
  const plugin = (enabled: 'true' | 'false') => arm('plugin inspect -f {{.Enabled}} loki', enabled);

  // A short readiness watch: an ok `up` hands off to one, and the 180s default would outlive the test.
  const boot = (docker: { dir: string }) => bootServer({ tag: 'loki', pathPrefix: docker.dir, readiness: { pollMs: 20, timeoutMs: 200 } });

  /** PUT the stack with COMPOSE, POST up, wait for the job to settle. */
  const deploy = async (s: Booted, id: string, orchestrator: 'compose' | 'swarm'): Promise<Job> => {
    const put = await fetch(`${s.base}/api/deployments/${id}`, {
      method: 'PUT',
      headers: s.H,
      body: JSON.stringify({ spec: `version: 1\nstack: ${id}\ncompose:\n  file: compose.yml\n  orchestrator: ${orchestrator}\naxes: []\n`, compose: COMPOSE }),
    });
    expect(put.status).toBe(201);
    const up = await fetch(`${s.base}/api/deployments/${id}/up`, { method: 'POST', headers: s.H });
    expect(up.status).toBe(202);
    const { job } = (await up.json()) as { job: { id: string } };
    return (await waitJob(s, job.id)) as Job;
  };

  /** The derived file docker was given: JSON despite the extension, and unredacted, as docker reads it. */
  const generated = (s: Booted, id: string) =>
    (JSON.parse(readFileSync(join(s.dataDir, 'deployments', id, 'compose.generated.yml'), 'utf8')) as { services: Record<string, { logging?: unknown }> }).services;

  const said = (job: Job) => (job.log ?? []).map((e) => e.message);

  // negative control: make autolabel.InjectLogging's `svc.Has("logging")` check always false — db's
  // json-file block is replaced by loki's and the db assertion fails.
  test('a compose deploy gives every service without its own logging the loki block', async () => {
    const docker = dockerShim(`${LOKI_SHIM}\n${plugin('true')}`);
    const s = await boot(docker);
    try {
      const job = await deploy(s, 'lg-compose', 'compose');
      expect(job.state).toBe('ok');
      const services = generated(s, 'lg-compose');
      // Stringified so the key order is asserted too: JSON.parse keeps the file's order.
      expect(JSON.stringify(services.web!.logging)).toBe(JSON.stringify(block('lg-compose', 'web')));
      expect(services.db!.logging).toEqual({ driver: 'json-file' });
      expect(said(job).some((m) => m.startsWith('logging: service db has its own logging:'))).toBe(true);
      expect(said(job).some((m) => m.startsWith('logging: service web'))).toBe(false);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 30_000);

  // negative control: make compose.ComposeUp's plugin check pass whatever docker prints — `compose up`
  // runs and the job is ok.
  test('a compose deploy on a host without the plugin fails before up, with the install line', async () => {
    const docker = dockerShim(`${LOKI_SHIM}\n${plugin('false')}`, { record: true });
    const s = await boot(docker);
    try {
      const job = await deploy(s, 'lg-noplugin', 'compose');
      expect(job.state).toBe('failed');
      // The step message says what is wrong, not how to fix it. The install line (longer than the
      // step message's 300-rune cap, stack.firstLine) goes to the job log whole.
      const step = job.outcome!.steps.find((st) => st.phase === 'compose');
      expect(step?.message).toStartWith('loki log plugin not installed');
      expect(said(job).some((m) => m.includes('docker plugin install grafana/loki-docker-driver:3.7.7-$arch --alias loki --grant-all-permissions LOG_LEVEL=warn'))).toBe(true);
      expect(docker.calls()).toContain('plugin inspect -f {{.Enabled}} loki');
      expect(docker.calls().filter((c) => c.startsWith('compose ') && c.includes(' up '))).toEqual([]);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 30_000);

  // negative control: drop compose.ComposeUp's note for a node whose LokiPlugin is false — the job log
  // never names wrk.
  test('a swarm deploy keeps the block through the conversion and names the node without the plugin', async () => {
    // NODE_PLUGINS is SWARM_SHIM's two nodes: mgr (n1) lists the plugin the way an enabled one
    // appears (`loki:latest`), wrk (n2) has none. No `plugin inspect` arm: the swarm branch never
    // runs the compose plugin check.
    const docker = dockerShim(`${SWARM_SHIM}\n${LOKI_SHIM}\n${NODE_PLUGINS}`, { record: true });
    const s = await boot(docker);
    try {
      const job = await deploy(s, 'lg-swarm', 'swarm');
      expect(job.state).toBe('ok');
      const services = generated(s, 'lg-swarm');
      expect(JSON.stringify(services.web!.logging)).toBe(JSON.stringify(block('lg-swarm', 'web')));
      expect(services.db!.logging).toEqual({ driver: 'json-file' });
      const nodeNotes = said(job).filter((m) => m.startsWith('logging: no loki plugin: '));
      expect(nodeNotes).toEqual(['logging: no loki plugin: wrk']);
      // Swarm keeps logged services off wrk by itself; the note says why, and the deploy still runs.
      expect(docker.calls().some((c) => c.startsWith('stack deploy '))).toBe(true);
    } finally {
      await s.stop();
      docker.remove();
    }
  }, 30_000);
});
