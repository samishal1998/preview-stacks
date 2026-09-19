/**
 * The CLI cases whose exact transcript is the contract — ONE table read by both the generator
 * (gen/goldens.ts) and the consumer (test/cli-goldens.test.ts).
 *
 * Determinism: every case pins what would otherwise vary — the data dir (`<DATA>`), the token, the
 * DNS credential, the Loki push password, the cloud-init password and SSH key — and the consumer
 * masks the implementation's own version to `<VERSION>` so a binary of any version is graded
 * against the same transcripts.
 */

/**
 * The fixed data dir every init case uses; masked to `<DATA>` in transcripts. A FIXED path, not
 * `$TMPDIR`: `init --dry-run` prints the byte size of the .env it would write, and that file holds
 * the path — so a path whose length depends on the machine is a golden that fails under turbo.
 */
export const DATA_DIR = '/tmp/pstack-golden-data';
export const TOKEN = 'golden-token-0123456789abcdef0123456789abcdef';
export const DNS_TOKEN = 'golden-dns-token-0123456789';
/** init takes it from PSTACK_LOKI_PASSWORD instead of generating one, so `.env` and the `{SHA}` label are fixed bytes. */
export const LOKI_PASSWORD = '0123456789abcdef0123456789abcdef';

export type Cell = { challenge: 'http01' | 'dns01'; ui: 'basic' | 'advanced'; orchestrator: 'compose' | 'swarm' };
export const CELLS: Cell[] = [];
for (const challenge of ['http01', 'dns01'] as const)
  for (const ui of ['basic', 'advanced'] as const)
    for (const orchestrator of ['compose', 'swarm'] as const) CELLS.push({ challenge, ui, orchestrator });
export const cellName = (c: Cell) => `${c.challenge}-${c.ui}-${c.orchestrator}`;
const initArgv = (c: Cell) => ['init', '--domain', 'preview.example.com', '--acme-email', 'ops@example.com', '--challenge', c.challenge, '--ui', c.ui, '--orchestrator', c.orchestrator, ...(c.challenge === 'dns01' ? ['--dns-provider', 'cloudflare'] : [])];
const initEnv = (c: Cell) => ({ PSTACK_DATA: DATA_DIR, PSTACK_TOKEN: TOKEN, ...(c.challenge === 'dns01' ? { PSTACK_DNS_TOKEN: DNS_TOKEN } : {}) });

/**
 * `--logging loki` on two cells, not eight: together they cover both TLS label forms (http01 adds
 * `tls.certresolver`) and both orchestrators, and logging varies with nothing else a cell varies.
 */
// negative control: in initctl.LokiService, drop the HTTP01 `tls.certresolver=le` label and rebuild —
// init-http01-basic-compose-loki fails on docker-compose.yml.
const LOKI_CELLS: Cell[] = [
  { challenge: 'http01', ui: 'basic', orchestrator: 'compose' },
  { challenge: 'dns01', ui: 'advanced', orchestrator: 'swarm' },
];

/** Arms for a docker that lets `init` finish: the health wait sees `healthy`, everything else succeeds silently. */
export const INIT_SHIM = [
  `  "compose -p pstack-control ps -q pstack") printf '%s\\n' 'c0ffee' ;;`,
  `  "inspect -f {{.State.Health.Status}} c0ffee") printf '%s\\n' 'healthy' ;;`,
  `  "info --format {{.Swarm.LocalNodeState}}") printf '%s\\n' 'active' ;;`,
].join('\n');

/** The swarm shim from features.test.ts — a two-node swarm with one stack. */
export const SWARM_SHIM = [
  `  "info --format {{json .Swarm}}") printf '%s\\n' '{"NodeID":"n1","NodeAddr":"10.0.0.1","LocalNodeState":"active","ControlAvailable":true,"RemoteManagers":[{"NodeID":"n1","Addr":"10.0.0.1:2377"}]}' ;;`,
  `  "node ls --format {{json .}}") printf '%s\\n' '{"ID":"n1","Hostname":"mgr","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}' '{"ID":"n2","Hostname":"wrk","Status":"Ready","Availability":"Active","ManagerStatus":"","EngineVersion":"28.0.1","Self":"false"}' ;;`,
  `  "swarm join-token -q worker") printf '%s\\n' 'SWMTKN-1-abc-def' ;;`,
].join('\n');

/** A docker that is NOT a manager. */
export const NO_SWARM_SHIM = `  "info --format {{json .Swarm}}") printf '%s\\n' '{"NodeID":"","NodeAddr":"","LocalNodeState":"inactive","ControlAvailable":false}' ;;`;

/** The control stack's loki container — what `inspect.LokiPushURL` reads to find logging is on. */
export const LOKI_SHIM = [
  `  "ps -aq --filter label=com.docker.compose.project=pstack-control") printf '%s\\n' 'l0k1' ;;`,
  `  "inspect l0k1") printf '%s\\n' '[{"Id":"l0k1","Name":"/pstack-control-loki-1","Config":{"Image":"grafana/loki:3.7.7","Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"loki","pstack.logging.push-url":"https://pstack:${LOKI_PASSWORD}@loki.preview.example.com/loki/api/v1/push"}},"State":{"Status":"running"}}]' ;;`,
].join('\n');

/** `docker node inspect` for SWARM_SHIM's two nodes: n1 carries the loki log plugin, n2 only overlay. */
export const NODE_PLUGINS = `  "node inspect --format {{json .}} n1 n2") printf '%s\\n' '{"ID":"n1","Description":{"Engine":{"Plugins":[{"Type":"Log","Name":"loki:latest"},{"Type":"Network","Name":"overlay"}]}}}' '{"ID":"n2","Description":{"Engine":{"Plugins":[{"Type":"Network","Name":"overlay"}]}}}' ;;`;

/** A `status` answer: one running container for the example's stack. */
export const STATUS_SHIM = `  "compose -p pr-1 -f docker-compose.preview.yml ps") printf '%s\\n' 'NAME        IMAGE   STATUS' 'pr-1-web-1  nginx   running' ;;`;

export type Case = {
  name: string;
  argv: string[];
  env?: Record<string, string>;
  /** Docker shim arms; the shim is first on PATH for this run. */
  shim?: string;
  /** Run from a fresh DATA_DIR (wiped first). */
  freshData?: boolean;
  /** Copy these files out of DATA_DIR into golden/render/<renderDir>/ after the run. */
  render?: { dir: string; files: string[] };
  /** Run this case AFTER the named one, in the same DATA_DIR (upgrade plans read init's output). */
  after?: string;
};

const EXAMPLE_ENV = { PR: '1', GIT_SHA: 'ci' };
const CLOUD_INIT = ['cloud-init', '--domain', 'preview.example.com', '--acme-email', 'ops@example.com', '--ssh-key', 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGoldenKeyForTheConformanceSuite0000000 golden@example', '--password', 'golden-dashboard-password', '-y'];

export const CASES: Case[] = [
  { name: 'help', argv: ['--help'] },
  { name: 'help-h', argv: ['-h'] },
  { name: 'version', argv: ['--version'] },
  { name: 'no-args', argv: [] },
  { name: 'unknown-command', argv: ['upgradee'] },
  { name: 'unknown-flag', argv: ['--bogus'] },
  { name: 'validate-example', argv: ['-f', 'examples/preview.yml', 'validate'], env: EXAMPLE_ENV },
  { name: 'validate-shared-example', argv: ['-f', 'examples/shared.yml', 'validate'] },
  { name: 'validate-missing-var', argv: ['-f', 'examples/preview.yml', 'validate'], env: { GIT_SHA: 'ci' } },
  { name: 'validate-missing-file', argv: ['-f', 'examples/nope.yml', 'validate'] },
  { name: 'validate-set-override', argv: ['-f', 'examples/preview.yml', 'validate', '--set', 'PR=42', '--set', 'GIT_SHA=abc'] },
  { name: 'up-dry-verbose', argv: ['-f', 'examples/preview.yml', 'up', '-n', '-v'], env: EXAMPLE_ENV },
  { name: 'up-dry-quiet', argv: ['-f', 'examples/preview.yml', 'up', '-n', '-q'], env: EXAMPLE_ENV },
  { name: 'down-dry-verbose', argv: ['-f', 'examples/preview.yml', 'down', '-n', '-v'], env: EXAMPLE_ENV },
  { name: 'down-dry-no-verify', argv: ['-f', 'examples/preview.yml', 'down', '-n', '-v', '--no-verify'], env: EXAMPLE_ENV },
  { name: 'verify-dry-verbose', argv: ['-f', 'examples/preview.yml', 'verify', '-n', '-v'], env: EXAMPLE_ENV },
  { name: 'up-dry-swarm', argv: ['-f', 'examples/preview.yml', 'up', '-n', '-v'], env: { ...EXAMPLE_ENV, PSTACK_ORCHESTRATOR: 'swarm' } },
  { name: 'down-dry-swarm', argv: ['-f', 'examples/preview.yml', 'down', '-n', '-v'], env: { ...EXAMPLE_ENV, PSTACK_ORCHESTRATOR: 'swarm' } },
  { name: 'status', argv: ['-f', 'examples/preview.yml', 'status'], env: EXAMPLE_ENV, shim: STATUS_SHIM },
  { name: 'dockerfile', argv: ['dockerfile'] },
  { name: 'dockerfile-ui', argv: ['dockerfile', '--ui'] },
  ...(['ubuntu', 'debian', 'fedora', 'suse', 'arch', 'alpine'] as const).map((distro) => ({
    name: `cloud-init-${distro}`,
    argv: [...CLOUD_INIT, '--distro', distro],
  })),
  { name: 'cloud-init-dns01-advanced-swarm', argv: [...CLOUD_INIT, '--challenge', 'dns01', '--dns-provider', 'cloudflare', '--ui', 'advanced', '--orchestrator', 'swarm', '--config-repo', 'https://github.com/example/previews.git'], env: { PSTACK_DNS_TOKEN: DNS_TOKEN } },
  { name: 'cloud-init-loki', argv: [...CLOUD_INIT, '--logging', 'loki'] },
  { name: 'cloud-init-bad-distro', argv: [...CLOUD_INIT, '--distro', 'plan9'] },
  { name: 'cloud-init-bad-challenge', argv: [...CLOUD_INIT, '--challenge', 'tls-alpn'] },
  ...CELLS.flatMap((c) => {
    const name = cellName(c);
    const argv = initArgv(c);
    const env = initEnv(c);
    return [
      { name: `init-dry-${name}`, argv: [...argv, '-n'], env, shim: INIT_SHIM, freshData: true } as Case,
      { name: `init-${name}`, argv, env, shim: INIT_SHIM, freshData: true, render: { dir: `control/${name}`, files: ['control/docker-compose.yml', 'control/.env', 'control/dns.env'] } } as Case,
      // PSTACK_INSTALL_DIR pinned: without it the plan derives the install directory from where
      // `pstack` sits on PATH, so the transcript said one thing on a machine that has it installed
      // and another (plus a two-line note) on one that does not — a golden that could only pass
      // where it was generated.
      { name: `upgrade-plan-${name}`, argv: ['upgrade', '-n', '--to', '0.29.1'], env: { PSTACK_DATA: DATA_DIR, PSTACK_INSTALL_DIR: '/usr/local/bin' }, after: `init-${name}` } as Case,
      { name: `ui-switch-dry-${name}`, argv: ['ui', c.ui === 'basic' ? 'advanced' : 'basic', '-n'], env: { PSTACK_DATA: DATA_DIR }, after: `init-${name}` } as Case,
    ];
  }),
  // Position is what counts (`after` is documentation): the row above is a dry run, so the data dir
  // still holds init-dns01-advanced-swarm, a logging-off host.
  { name: 'logging-loki-dry-dns01-advanced-swarm', argv: ['logging', 'loki', '-n'], env: { PSTACK_DATA: DATA_DIR }, after: 'init-dns01-advanced-swarm' },
  ...LOKI_CELLS.flatMap((c) => {
    const name = `${cellName(c)}-loki`;
    const argv = [...initArgv(c), '--logging', 'loki'];
    const env = { ...initEnv(c), PSTACK_LOKI_PASSWORD: LOKI_PASSWORD };
    return [
      { name: `init-dry-${name}`, argv: [...argv, '-n'], env, shim: INIT_SHIM, freshData: true } as Case,
      // negative control: drop Init's control/grafana/datasources/loki.yaml write and rebuild —
      // init-<cell>-loki fails: the file's golden exists and the live file does not.
      { name: `init-${name}`, argv, env, shim: INIT_SHIM, freshData: true, render: { dir: `control/${name}`, files: ['control/docker-compose.yml', 'control/.env', 'control/dns.env', 'control/loki/config.yaml', 'control/grafana/datasources/loki.yaml'] } } as Case,
      { name: `upgrade-plan-${name}`, argv: ['upgrade', '-n', '--to', '0.29.1'], env: { PSTACK_DATA: DATA_DIR, PSTACK_INSTALL_DIR: '/usr/local/bin' }, after: `init-${name}` } as Case,
      { name: `logging-off-dry-${name}`, argv: ['logging', 'off', '-n'], env: { PSTACK_DATA: DATA_DIR }, after: `init-${name}` } as Case,
    ];
  }),
  { name: 'init-generated-token', argv: ['init', '--domain', 'preview.example.com', '--acme-email', 'ops@example.com'], env: { PSTACK_DATA: DATA_DIR }, shim: INIT_SHIM, freshData: true },
  { name: 'init-missing-domain', argv: ['init', '--acme-email', 'ops@example.com'], env: { PSTACK_DATA: DATA_DIR } },
  { name: 'init-bad-ui', argv: ['init', '--domain', 'p.example.com', '--acme-email', 'o@example.com', '--ui', 'fancy'], env: { PSTACK_DATA: DATA_DIR } },
  { name: 'upgrade-no-control', argv: ['upgrade', '-n'], env: { PSTACK_DATA: DATA_DIR }, freshData: true },
  { name: 'upgrade-bad-target', argv: ['upgrade', '-n', '--to', 'x; rm -rf /'], env: { PSTACK_DATA: DATA_DIR }, after: 'init-http01-basic-compose' },
  { name: 'ui-usage', argv: ['ui'], env: { PSTACK_DATA: DATA_DIR } },
  { name: 'swarm-status', argv: ['swarm'], shim: SWARM_SHIM },
  // Logging on: mgr lists the plugin (`loki:latest`, as an enabled one appears), wrk has none — the
  // report names wrk and the line to run there.
  // negative control: drop the swarm.MarkLokiPlugins call from run.go's `swarm status` — the
  // `no loki plugin: wrk` lines vanish and this golden fails.
  { name: 'swarm-status-loki', argv: ['swarm'], shim: [SWARM_SHIM, LOKI_SHIM, NODE_PLUGINS].join('\n') },
  { name: 'swarm-status-inactive', argv: ['swarm', 'status'], shim: NO_SWARM_SHIM },
  ...(['command', 'script', 'cloud-config', 'token'] as const).map((format) => ({ name: `swarm-join-${format}`, argv: ['swarm', 'join', '--format', format], shim: SWARM_SHIM })),
  { name: 'swarm-join-cloud-config-alpine', argv: ['swarm', 'join', '--format', 'cloud-config', '--distro', 'alpine'], shim: SWARM_SHIM },
  // Logging on: both forms install the plugin before the join (cloud-config's step is a literal block).
  { name: 'swarm-join-script-loki', argv: ['swarm', 'join', '--format', 'script'], shim: [SWARM_SHIM, LOKI_SHIM].join('\n') },
  { name: 'swarm-join-cloud-config-loki', argv: ['swarm', 'join', '--format', 'cloud-config'], shim: [SWARM_SHIM, LOKI_SHIM].join('\n') },
  { name: 'swarm-join-bad-format', argv: ['swarm', 'join', '--format', 'pdf'], shim: SWARM_SHIM },
  { name: 'swarm-join-not-manager', argv: ['swarm', 'join'], shim: NO_SWARM_SHIM },
  { name: 'swarm-bad-sub', argv: ['swarm', 'dance'], shim: SWARM_SHIM },
  { name: 'serve-interlock', argv: ['serve'], env: { PSTACK_HOST: '0.0.0.0', PSTACK_DATA: DATA_DIR } },
  { name: 'healthcheck-dead', argv: ['healthcheck'], env: { PSTACK_PORT: '1' } },
];
