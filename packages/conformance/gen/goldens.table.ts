/**
 * The CLI cases whose exact transcript is the contract — ONE table read by both the generator
 * (gen/goldens.ts, run against the Bun reference) and the consumer (test/cli-goldens.test.ts, run
 * against whichever implementation PSTACK_IMPL names).
 *
 * Determinism: every case pins what would otherwise vary — the data dir (`<DATA>`), the token, the
 * DNS credential, the cloud-init password and SSH key — and the consumer masks the implementation's
 * own version to `<VERSION>` so a 0.29.0 binary is graded against transcripts made by 0.28.0.
 *
 * `goDivergent` names the lines that differ BY DESIGN between the TypeScript and Go builds (the Bun
 * install block in cloud-init, the npm-based Dockerfiles and upgrade step): in go mode those lines
 * are dropped from both sides before comparing, and a Go self-golden under golden/render-go pins
 * them separately once packaging decides them.
 */

/**
 * The fixed data dir every init case uses; masked to `<DATA>` in transcripts. A FIXED path, not
 * `$TMPDIR`: `init --dry-run` prints the byte size of the .env it would write, and that file holds
 * the path — so a path whose length depends on the machine is a golden that fails under turbo.
 */
export const DATA_DIR = '/tmp/pstack-golden-data';
export const TOKEN = 'golden-token-0123456789abcdef0123456789abcdef';
export const DNS_TOKEN = 'golden-dns-token-0123456789';

export type Cell = { challenge: 'http01' | 'dns01'; ui: 'basic' | 'advanced'; orchestrator: 'compose' | 'swarm' };
export const CELLS: Cell[] = [];
for (const challenge of ['http01', 'dns01'] as const)
  for (const ui of ['basic', 'advanced'] as const)
    for (const orchestrator of ['compose', 'swarm'] as const) CELLS.push({ challenge, ui, orchestrator });
export const cellName = (c: Cell) => `${c.challenge}-${c.ui}-${c.orchestrator}`;

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
  /** Lines matching this diverge between the TS and Go builds by design — dropped in go mode. */
  goDivergent?: RegExp;
  /** Run this case AFTER the named one, in the same DATA_DIR (upgrade plans read init's output). */
  after?: string;
};

const EXAMPLE_ENV = { PR: '1', GIT_SHA: 'ci' };
const CLOUD_INIT = ['cloud-init', '--domain', 'preview.example.com', '--acme-email', 'ops@example.com', '--ssh-key', 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGoldenKeyForTheConformanceSuite0000000 golden@example', '--password', 'golden-dashboard-password', '-y'];
const BUN_LINES = /bun|BUN_|@samyx\/preview-stacks|unzip/;

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
  { name: 'dockerfile', argv: ['dockerfile'], goDivergent: BUN_LINES },
  { name: 'dockerfile-ui', argv: ['dockerfile', '--ui'], goDivergent: BUN_LINES },
  ...(['ubuntu', 'debian', 'fedora', 'suse', 'arch', 'alpine'] as const).map((distro) => ({
    name: `cloud-init-${distro}`,
    argv: [...CLOUD_INIT, '--distro', distro],
    goDivergent: BUN_LINES,
  })),
  { name: 'cloud-init-dns01-advanced-swarm', argv: [...CLOUD_INIT, '--challenge', 'dns01', '--dns-provider', 'cloudflare', '--ui', 'advanced', '--orchestrator', 'swarm', '--config-repo', 'https://github.com/example/previews.git'], env: { PSTACK_DNS_TOKEN: DNS_TOKEN }, goDivergent: BUN_LINES },
  { name: 'cloud-init-bad-distro', argv: [...CLOUD_INIT, '--distro', 'plan9'] },
  { name: 'cloud-init-bad-challenge', argv: [...CLOUD_INIT, '--challenge', 'tls-alpn'] },
  ...CELLS.flatMap((c) => {
    const name = cellName(c);
    const argv = ['init', '--domain', 'preview.example.com', '--acme-email', 'ops@example.com', '--challenge', c.challenge, '--ui', c.ui, '--orchestrator', c.orchestrator, ...(c.challenge === 'dns01' ? ['--dns-provider', 'cloudflare'] : [])];
    const env = { PSTACK_DATA: DATA_DIR, PSTACK_TOKEN: TOKEN, ...(c.challenge === 'dns01' ? { PSTACK_DNS_TOKEN: DNS_TOKEN } : {}) };
    return [
      { name: `init-dry-${name}`, argv: [...argv, '-n'], env, shim: INIT_SHIM, freshData: true } as Case,
      { name: `init-${name}`, argv, env, shim: INIT_SHIM, freshData: true, render: { dir: `control/${name}`, files: ['control/docker-compose.yml', 'control/.env', 'control/dns.env'] } } as Case,
      { name: `upgrade-plan-${name}`, argv: ['upgrade', '-n', '--to', '0.28.1'], env: { PSTACK_DATA: DATA_DIR }, after: `init-${name}`, goDivergent: BUN_LINES } as Case,
      { name: `ui-switch-dry-${name}`, argv: ['ui', c.ui === 'basic' ? 'advanced' : 'basic', '-n'], env: { PSTACK_DATA: DATA_DIR }, after: `init-${name}` } as Case,
    ];
  }),
  { name: 'init-generated-token', argv: ['init', '--domain', 'preview.example.com', '--acme-email', 'ops@example.com'], env: { PSTACK_DATA: DATA_DIR }, shim: INIT_SHIM, freshData: true },
  { name: 'init-missing-domain', argv: ['init', '--acme-email', 'ops@example.com'], env: { PSTACK_DATA: DATA_DIR } },
  { name: 'init-bad-ui', argv: ['init', '--domain', 'p.example.com', '--acme-email', 'o@example.com', '--ui', 'fancy'], env: { PSTACK_DATA: DATA_DIR } },
  { name: 'upgrade-no-control', argv: ['upgrade', '-n'], env: { PSTACK_DATA: DATA_DIR }, freshData: true },
  { name: 'upgrade-to-go-release', argv: ['upgrade', '--to', '0.29.0'], env: { PSTACK_DATA: DATA_DIR }, after: 'init-http01-basic-compose' },
  { name: 'upgrade-bad-target', argv: ['upgrade', '-n', '--to', 'x; rm -rf /'], env: { PSTACK_DATA: DATA_DIR }, after: 'init-http01-basic-compose', goDivergent: BUN_LINES },
  { name: 'ui-usage', argv: ['ui'], env: { PSTACK_DATA: DATA_DIR } },
  { name: 'swarm-status', argv: ['swarm'], shim: SWARM_SHIM },
  { name: 'swarm-status-inactive', argv: ['swarm', 'status'], shim: NO_SWARM_SHIM },
  ...(['command', 'script', 'cloud-config', 'token'] as const).map((format) => ({ name: `swarm-join-${format}`, argv: ['swarm', 'join', '--format', format], shim: SWARM_SHIM, goDivergent: format === 'cloud-config' ? BUN_LINES : undefined })),
  { name: 'swarm-join-cloud-config-alpine', argv: ['swarm', 'join', '--format', 'cloud-config', '--distro', 'alpine'], shim: SWARM_SHIM, goDivergent: BUN_LINES },
  { name: 'swarm-join-bad-format', argv: ['swarm', 'join', '--format', 'pdf'], shim: SWARM_SHIM },
  { name: 'swarm-join-not-manager', argv: ['swarm', 'join'], shim: NO_SWARM_SHIM },
  { name: 'swarm-bad-sub', argv: ['swarm', 'dance'], shim: SWARM_SHIM },
  { name: 'serve-interlock', argv: ['serve'], env: { PSTACK_HOST: '0.0.0.0', PSTACK_DATA: DATA_DIR } },
  { name: 'healthcheck-dead', argv: ['healthcheck'], env: { PSTACK_PORT: '1' } },
];
