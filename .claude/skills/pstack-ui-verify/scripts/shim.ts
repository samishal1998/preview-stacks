#!/usr/bin/env bun
/**
 * Write a fake `docker` to <dir>/docker, built from the conformance fixtures, so `pstack serve`
 * sees a plausible host without a real daemon.
 *
 *   bun shim.ts <dir> [swarm] [loki] [grafana] [plugins]     no arms = swarm loki plugins
 *
 *   swarm    SWARM_SHIM: a two-node swarm, this node the leader (Swarm page, join commands)
 *   loki     LOKI_SHIM's control container: logging on (the push-url label)
 *   grafana  the grafana control container from test/api-grafana.test.ts: the Grafana nav link
 *   plugins  NODE_PLUGINS: n1 has the loki log plugin, n2 does not (only meaningful with swarm)
 *
 * The default set is golden case `swarm-status-loki`, a combination known to work. Anything else
 * docker is asked exits 0 with no output (the harness's ALWAYS_OK), and every call is appended to
 * <dir>/calls.log: the argv the API actually issued.
 *
 * loki and grafana are MERGED, not concatenated: both answer the control project's `docker ps`, a
 * `case` takes the first matching arm, and pstack inspects every id in one `docker inspect a b`
 * call. So they become one ps arm printing both ids and one inspect arm returning both objects,
 * plus the `compose -p pstack-control ps --format json` answer the dashboard's control card reads.
 * With neither arm, that card reports "no containers in this project" — true of this fake host.
 */
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { LOKI_SHIM, NODE_PLUGINS, SWARM_SHIM } from '../../../../packages/conformance/gen/goldens.table.ts';
import { arm } from '../../../../packages/conformance/harness/docker-shim.ts';

const KNOWN = ['swarm', 'loki', 'grafana', 'plugins'];
const [dir, ...picked] = process.argv.slice(2);
const bad = picked.filter((a) => !KNOWN.includes(a));
if (!dir || bad.length) {
  console.error(`usage: bun shim.ts <dir> [${KNOWN.join('] [')}]${bad.length ? `\nunknown arm: ${bad.join(', ')}` : ''}`);
  process.exit(2);
}
const want = new Set(picked.length ? picked : ['swarm', 'loki', 'plugins']);

// LOKI_SHIM's `inspect l0k1` arm carries the container as a one-element JSON array in single quotes.
const lokiJson = /'(\[\{.*\}\])'/.exec(LOKI_SHIM)?.[1];
if (!lokiJson) throw new Error('LOKI_SHIM changed shape: no inspect JSON found');
const LOKI = JSON.parse(lokiJson)[0];
// GRAFANA_SHIM is not exported (it lives in a test file); this is its container, verbatim.
const GRAFANA = {
  Id: 'gr4f',
  Name: '/pstack-control-grafana-1',
  Config: { Image: 'grafana/grafana:13.2.1', Labels: { 'com.docker.compose.project': 'pstack-control', 'com.docker.compose.service': 'grafana' } },
  State: { Status: 'running' },
};

const control = [...(want.has('loki') ? [LOKI] : []), ...(want.has('grafana') ? [GRAFANA] : [])];
const ids = control.map((c) => c.Id);
const arms = [
  ...(want.has('swarm') ? [SWARM_SHIM] : []),
  ...(want.has('plugins') ? [NODE_PLUGINS] : []),
  ...(control.length
    ? [
        arm('ps -aq --filter label=com.docker.compose.project=pstack-control', ids.join('\n')),
        arm(`inspect ${ids.join(' ')}`, JSON.stringify(control)),
        // GET /api/control (the dashboard's control card) asks compose instead; same containers, NDJSON.
        arm(
          'compose -p pstack-control ps --format json',
          control.map((c) => JSON.stringify({ Service: c.Config.Labels['com.docker.compose.service'], State: c.State.Status, Health: '', Image: c.Config.Image })).join('\n'),
        ),
      ]
    : []),
].join('\n');

mkdirSync(dir, { recursive: true });
const log = join(dir, 'calls.log');
// printf, never echo: sh's echo mangles backslashes (docker-shim.ts header).
writeFileSync(join(dir, 'docker'), `#!/bin/sh\nprintf '%s\\n' "$*" >> ${JSON.stringify(log)}\ncase "$*" in\n${arms}\n  *) exit 0 ;;\nesac\n`, { mode: 0o755 });
console.log(`${join(dir, 'docker')}: ${[...want].join(' ')}`);
