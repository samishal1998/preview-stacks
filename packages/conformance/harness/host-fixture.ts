/**
 * Shared between gen/host-fixture.ts (the producer) and test/host-fixture.test.ts (the consumer):
 * the docker the fixture host "has", the routes whose responses are recorded, and the masks.
 *
 * The fixture host is a swarm manager running two stacks (pr-1 awake, sleepy — which then goes to
 * sleep). The shim answers exactly what the API asks for those, and `yes` to everything else.
 */
export const FIXTURE_TOKEN = 'fixture-token-0123456789abcdef0123456789abcdef';

const svc = (stack: string, id: string) =>
  `[{"ID":"${id}","UpdatedAt":"2026-08-20T10:00:00Z","Spec":{"Name":"${stack}_app","Labels":{"com.docker.stack.namespace":"${stack}","traefik.enable":"true","traefik.swarm.network":"preview-ingress","traefik.http.routers.app-${stack}.rule":"Host(\`app-${stack}.preview.example.com\`)","traefik.http.routers.app-${stack}.entrypoints":"websecure","traefik.http.routers.app-${stack}.tls":"true","traefik.http.routers.app-${stack}.service":"app-${stack}","traefik.http.services.app-${stack}.loadbalancer.server.port":"80"},"TaskTemplate":{"ContainerSpec":{"Image":"nginx:alpine"},"Networks":[{"Target":"net1"}]}}}]`;
const ctr = (stack: string, id: string) =>
  `[{"Id":"${id}","Name":"/${stack}_app.1.task${id.slice(0, 4)}","Config":{"Image":"nginx:alpine","Labels":{"com.docker.stack.namespace":"${stack}","com.docker.swarm.service.name":"${stack}_app","com.docker.swarm.task.id":"task${id.slice(0, 4)}","com.docker.swarm.node.id":"n1"}},"State":{"Status":"running","StartedAt":"2026-08-20T10:01:00Z"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"10.0.1.5"}},"Ports":{}}}]`;

export const FIXTURE_SHIM = [
  `  "info --format {{json .Swarm}}") printf '%s\\n' '{"NodeID":"n1","NodeAddr":"10.0.0.1","LocalNodeState":"active","ControlAvailable":true,"RemoteManagers":[{"NodeID":"n1","Addr":"10.0.0.1:2377"}]}' ;;`,
  `  "info --format {{.Swarm.LocalNodeState}}") printf '%s\\n' 'active' ;;`,
  `  "node ls --format {{json .}}") printf '%s\\n' '{"ID":"n1","Hostname":"fixture-mgr","Status":"Ready","Availability":"Active","ManagerStatus":"Leader","EngineVersion":"28.0.1","Self":"true"}' ;;`,
  `  "swarm join-token -q worker") printf '%s\\n' 'SWMTKN-1-fixture-token' ;;`,
  `  "compose ls --all --format json") printf '%s\\n' '[{"Name":"pstack-control","Status":"running(2)"}]' ;;`,
  `  "stack ls --format {{.Name}}") printf '%s\\n' 'pr-1' ;;`,
  `  "network ls --format {{.ID}} {{.Name}}") printf '%s\\n' 'net1 preview-ingress' 'net2 preview-shared' ;;`,
  // pr-1: one service, one running task
  `  "service ls -q --filter label=com.docker.stack.namespace=pr-1") printf '%s\\n' 'svcpr1' ;;`,
  `  "service inspect svcpr1") printf '%s\\n' '${svc('pr-1', 'svcpr1')}' ;;`,
  `  "stack ps pr-1 --no-trunc --filter desired-state=running --format {{json .}}") printf '%s\\n' '{"ID":"taskaaa1","Name":"pr-1_app.1","Image":"nginx:alpine","Node":"fixture-mgr","DesiredState":"Running","CurrentState":"Running 2 minutes ago"}' ;;`,
  `  "ps -aq --filter label=com.docker.stack.namespace=pr-1") printf '%s\\n' 'aaa111aaa111' ;;`,
  `  "inspect aaa111aaa111") printf '%s\\n' '${ctr('pr-1', 'aaa111aaa111')}' ;;`,
  // sleepy: answered while it is up, so the sleep captures its hostnames
  `  "service ls -q --filter label=com.docker.stack.namespace=sleepy") printf '%s\\n' 'svcslp' ;;`,
  `  "service inspect svcslp") printf '%s\\n' '${svc('sleepy', 'svcslp')}' ;;`,
  `  "stack ps sleepy --no-trunc --filter desired-state=running --format {{json .}}") printf '%s\\n' '{"ID":"taskbbb1","Name":"sleepy_app.1","Image":"nginx:alpine","Node":"fixture-mgr","DesiredState":"Running","CurrentState":"Running 1 minute ago"}' ;;`,
  `  "ps -aq --filter label=com.docker.stack.namespace=sleepy") printf '%s\\n' 'bbb222bbb222' ;;`,
  `  "inspect bbb222bbb222") printf '%s\\n' '${ctr('sleepy', 'bbb222bbb222')}' ;;`,
  // every traefik-labelled service, for the router-collision scan
  `  "service ls -q --filter label=traefik.enable=true") printf '%s\\n' 'svcpr1' 'svcslp' ;;`,
  // the control stack, for challenge detection
  `  "ps -aq --filter label=com.docker.compose.project=pstack-control") printf '%s\\n' 'cccc11cccc11' ;;`,
  `  "inspect cccc11cccc11") printf '%s\\n' '[{"Id":"cccc11cccc11","Name":"/pstack-control-traefik-1","Config":{"Image":"traefik:v3.6.1","Cmd":["--providers.docker=true","--certificatesresolvers.le.acme.dnschallenge=true"],"Labels":{"com.docker.compose.project":"pstack-control","com.docker.compose.service":"traefik"}},"Args":[],"State":{"Status":"running","Health":{"Status":"healthy"}},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"10.0.1.2"}},"Ports":{"80/tcp":[{"HostPort":"80"}]}}}]' ;;`,
  // docker stack rm's wait loop polls services; answer "none left" so sleep/down finish at once
  `  "stack services sleepy --format {{.Name}}") exit 0 ;;`,
  `  "stack services pr-1 --format {{.Name}}") exit 0 ;;`,
].join('\n');

/** The routes whose reference responses are recorded. `as` picks the credential. */
export const ROUTES: Array<{ name: string; path: string; as?: 'admin' | 'bob'; volatile?: RegExp }> = [
  { name: 'health', path: '/api/health' },
  { name: 'me-admin', path: '/api/auth/me', as: 'admin' },
  { name: 'me-bob', path: '/api/auth/me', as: 'bob' },
  { name: 'me-root', path: '/api/auth/me' },
  { name: 'users', path: '/api/users' },
  { name: 'tokens-admin', path: '/api/tokens', as: 'admin' },
  { name: 'host-vars', path: '/api/host-vars' },
  { name: 'registries', path: '/api/registries' },
  // a file's mtime is the copy's, not the fixture's
  { name: 'routing', path: '/api/routing', volatile: /("updatedAt": )\d+/g },
  { name: 'routing-extra', path: '/api/routing/extra.yml' },
  { name: 'specs', path: '/api/specs' },
  { name: 'spec-web', path: '/api/specs/web' },
  { name: 'deployments', path: '/api/deployments?PR=2' },
  { name: 'deployment-pr-1', path: '/api/deployments/pr-1' },
  { name: 'deployment-pr-2', path: '/api/deployments/pr-2?PR=2' },
  { name: 'deployment-shared-db', path: '/api/deployments/shared-db' },
  { name: 'deployment-sleepy', path: '/api/deployments/sleepy' },
  { name: 'runtime-pr-1', path: '/api/deployments/pr-1/runtime' },
  { name: 'runtime-sleepy', path: '/api/deployments/sleepy/runtime' },
  { name: 'source-pr-1', path: '/api/deployments/pr-1/source' },
  { name: 'notifiers', path: '/api/notifiers' },
  { name: 'sso-config', path: '/api/sso/config' },
  { name: 'swarm', path: '/api/swarm' },
  { name: 'control', path: '/api/control' },
  { name: 'routing-live', path: '/api/routing/live' },
  { name: 'terminal-sessions', path: '/api/terminal-sessions' },
];

/**
 * What varies between the producer's run and a consumer's: the data dir, the version, and the
 * handful of timestamps the act of READING refreshes (a token's lastUsedAt). Everything else in a
 * stored row is the fixture's own bytes.
 */
export function maskHost(text: string, version: string, dataDir: string, volatile?: RegExp): string {
  return (volatile ? text.replace(volatile, '$1"<VOLATILE>"') : text)
    .split(dataDir).join('<DATA>')
    // receivers, the fake provider and the server itself all live on 127.0.0.1:<ephemeral>
    .replace(/127\.0\.0\.1:\d+/g, '127.0.0.1:<PORT>')
    .split(JSON.stringify(version)).join('"<VERSION>"')
    // null before the first use, a timestamp after — and a consumer's own earlier test is a use
    .replace(/"lastUsedAt": ?(\d{13}|null)/g, '"lastUsedAt": "<TS>"');
}
