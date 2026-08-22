/**
 * The differential scenarios: linear request sequences that walk every route group. Each runs
 * twice — impl A, then impl B — over the same data path, and the traces must match step for step.
 *
 * A scenario never branches on what it gets back (that would make the trace depend on the
 * implementation); it records whatever came and lets the comparison judge.
 */
import type { Scenario } from '../../harness/diff.ts';
import { FIXTURE_SHIM } from '../../harness/host-fixture.ts';
import { SWARM_SHIM } from '../../gen/goldens.table.ts';

const SPEC = (stack: string, extra = '') =>
  `version: 1\nstack: ${stack}\ncompose:\n  file: docker-compose.yml\n  profiles: []\n${extra}axes:\n  - name: db\n    up: "echo DB_URL=postgres://db/${stack}"\n    down: "true"\n    assert_gone: "true"\n    assert_live: "true"\n`;
const COMPOSE = 'services:\n  app:\n    image: nginx:alpine\n    labels:\n      - pstack.routing.port=80\n    restart: always\n    mem_limit: 128m\n';

const COMPOSE_SHIM = [
  `  "compose ls --all --format json") printf '%s\\n' '[{"Name":"pr-1","Status":"running(1)"}]' ;;`,
  `  "stack ls --format {{.Name}}") exit 1 ;;`,
  `  "info --format {{json .Swarm}}") printf '%s\\n' '{"NodeID":"","LocalNodeState":"inactive","ControlAvailable":false}' ;;`,
  `  "ps -aq --filter label=com.docker.compose.project=pr-1") printf '%s\\n' 'c0ffee123456' ;;`,
  `  "inspect c0ffee123456") printf '%s\\n' '[{"Id":"c0ffee123456","Name":"/pr-1-app-1","Config":{"Image":"nginx:alpine","Labels":{"com.docker.compose.service":"app","com.docker.compose.project":"pr-1","traefik.enable":"true","traefik.http.routers.app-pr-1.rule":"Host(\`app-pr-1.example.com\`)","traefik.http.services.app-pr-1.loadbalancer.server.port":"80"}},"State":{"Status":"running","StartedAt":"2026-08-20T10:00:00Z"},"NetworkSettings":{"Networks":{"preview-ingress":{"IPAddress":"172.20.0.5"}},"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]}}}]' ;;`,
  `  "compose -p pr-1 -f "*" ps --format json") printf '%s\\n' '[{"Service":"app","Name":"pr-1-app-1","State":"running","Health":"","Image":"nginx:alpine"}]' ;;`,
  `  "ps -aq --filter label=com.docker.compose.project=pstack-control") exit 0 ;;`,
].join('\n');

export const SCENARIOS: Scenario[] = [
  {
    name: 'ui-and-health',
    async run(s) {
      await s.fetch('GET', '/api/health', { headers: { authorization: '' } });
      await s.fetch('GET', '/', { headers: { authorization: '' } });
      await s.fetch('GET', '/deployments/pr-1/overview', { headers: { authorization: '' } });
      await s.fetch('GET', '/deployments/pr-1/public-logs-view', { headers: { authorization: '' } });
      await s.fetch('GET', '/api/nope');
      await s.fetch('DELETE', '/api/jobs');
      await s.fetch('GET', '/api/deployments', { headers: { authorization: 'Bearer wrong' } });
    },
  },
  {
    name: 'auth-users-tokens',
    async run(s) {
      await s.fetch('GET', '/api/auth/me');
      await s.fetch('POST', '/api/auth/bootstrap', { body: { username: 'admin', password: 'admin-password-1' } });
      await s.fetch('POST', '/api/auth/bootstrap', { body: { username: 'again', password: 'admin-password-1' } });
      await s.fetch('POST', '/api/auth/login', { body: { username: 'admin', password: 'nope' } });
      const login = await s.fetch('POST', '/api/auth/login', { body: { username: 'admin', password: 'admin-password-1' } });
      const cookie = /pstack_session=([^;]+)/.exec(login.headers.get('set-cookie') ?? '')?.[1] ?? '';
      const asAdmin = { authorization: '', cookie: `pstack_session=${cookie}` };
      await s.fetch('GET', '/api/auth/me', { headers: asAdmin });
      await s.fetch('POST', '/api/users', { body: { username: 'bob', password: 'bob-password-11', email: 'bob@example.com' } });
      await s.fetch('POST', '/api/users', { body: { username: 'bob', password: 'bob-password-11' } });
      await s.fetch('POST', '/api/users', { body: { username: 'Bad Name', password: 'x' } });
      await s.fetch('GET', '/api/users');
      await s.fetch('POST', '/api/tokens', { body: { name: 'ci' }, headers: asAdmin });
      await s.fetch('POST', '/api/tokens', { body: { name: '' }, headers: asAdmin });
      await s.fetch('POST', '/api/tokens', { body: { name: 'root-has-none' } });
      await s.fetch('GET', '/api/tokens', { headers: asAdmin });
      await s.fetch('PUT', '/api/users/2/password', { body: { password: 'short' } });
      await s.fetch('PUT', '/api/users/2/password', { body: { password: 'bob-password-22' } });
      await s.fetch('PUT', '/api/users/99/password', { body: { password: 'bob-password-22' } });
      await s.fetch('DELETE', '/api/users/2');
      await s.fetch('DELETE', '/api/users/1');
      await s.fetch('POST', '/api/auth/logout', { headers: asAdmin });
      await s.fetch('GET', '/api/auth/me', { headers: asAdmin });
      // X-Forwarded-* decide the cookie's Secure flag and every absolute URL the API builds
      await s.fetch('POST', '/api/auth/login', { body: { username: 'admin', password: 'admin-password-1' }, headers: { 'x-forwarded-proto': 'https' } });
      await s.fetch('GET', '/api/sso/config', { headers: { 'x-forwarded-proto': 'https', 'x-forwarded-host': 'preview.example.com, internal' } });
    },
  },
  {
    name: 'deployments-crud-and-jobs',
    shim: COMPOSE_SHIM,
    async run(s) {
      await s.fetch('GET', '/api/deployments');
      await s.fetch('PUT', '/api/deployments/pr-1', { raw: 'not json' });
      await s.fetch('PUT', '/api/deployments/pr-1', { body: {} });
      await s.fetch('PUT', '/api/deployments/pr-1', { body: { spec: 'version: 1\nstack: ${PR}\n', compose: COMPOSE } });
      await s.fetch('PUT', '/api/deployments/pr-1', { body: { spec: SPEC('pr-1'), compose: COMPOSE, env: { PR: 1, FLAG: true } } });
      await s.fetch('PUT', '/api/deployments/pr-1', { body: { spec: SPEC('pr-1'), compose: COMPOSE } });
      await s.tick();
      await s.fetch('PUT', '/api/deployments/pr-1b', { body: { spec: SPEC('pr-1'), compose: COMPOSE } });
      await s.fetch('PUT', '/api/deployments/a%2Fb', { body: { spec: SPEC('x') } });
      await s.fetch('GET', '/api/deployments');
      await s.fetch('GET', '/api/deployments/pr-1');
      await s.fetch('GET', '/api/deployments/pr-1/source');
      await s.fetch('GET', '/api/deployments/nope');
      await s.fetch('PATCH', '/api/deployments/pr-1');
      await s.fetch('GET', '/api/deployments/pr-1/up');
      const up = await s.fetch('POST', '/api/deployments/pr-1/up');
      await s.fetch('POST', '/api/deployments/pr-1/up');
      const upJob = await s.waitJob((up.body.job as { id: string }).id);
      void upJob;
      await s.fetch('GET', '/api/jobs');
      await s.fetch('GET', '/api/deployments/pr-1/runtime');
      await s.fetch('GET', '/api/deployments/pr-1/readiness?wait=1');
      await s.fetch('GET', '/api/deployments/pr-1/logs?tail=1.5&service=app');
      await s.fetch('GET', '/api/deployments/pr-1/logs?service=bad%20name');
      await s.fetch('POST', '/api/deployments/pr-1/containers/pr-1-app-1/restart?grace=9999');
      await s.fetch('POST', '/api/deployments/pr-1/containers/evil/stop');
      await s.fetch('GET', '/api/deployments/pr-1/logs/stream?tail=5');
      const verify = await s.fetch('POST', '/api/deployments/pr-1/verify');
      await s.waitJob((verify.body.job as { id: string }).id);
      await s.fetch('DELETE', '/api/deployments/pr-1');
      const down = await s.fetch('POST', '/api/deployments/pr-1/down', { body: {} });
      await s.waitJob((down.body.job as { id: string }).id);
      await s.fetch('GET', '/api/jobs?x=1&x=2');
      await s.fetch('GET', '/api/jobs/nope');
      await s.fetch('POST', '/api/jobs/nope/cancel');
      await s.fetch('GET', `/api/jobs/${(down.body.job as { id: string }).id}/stream`);
      await s.fetch('DELETE', '/api/deployments/pr-1b');
    },
  },
  {
    name: 'empty-shapes',
    shim: `  "compose -p bare -f "*" ps --format json") printf '%s\\n' '[]' ;;\n  "compose ls --all --format json") printf '%s\\n' '[]' ;;`,
    async run(s) {
      await s.fetch('PUT', '/api/deployments/bare', { body: { spec: 'version: 1\nstack: bare\n' } });
      await s.fetch('GET', '/api/deployments/bare');
      await s.fetch('GET', '/api/deployments/bare/runtime');
      await s.fetch('GET', '/api/deployments/bare/readiness');
      await s.fetch('POST', '/api/deployments/bare/sleep');
      await s.fetch('GET', '/api/deployments/bare/logs');
      const v = await s.fetch('POST', '/api/deployments/bare/verify');
      await s.waitJob((v.body.job as { id: string }).id);
      await s.fetch('GET', '/api/routing');
      await s.fetch('GET', '/api/routing/live');
      await s.fetch('GET', '/api/registries');
      await s.fetch('GET', '/api/host-vars');
      await s.fetch('GET', '/api/specs');
      await s.fetch('GET', '/api/notifiers');
      await s.fetch('GET', '/api/terminal-sessions');
      await s.fetch('GET', '/api/swarm');
      await s.fetch('GET', '/api/control');
      await s.fetch('GET', '/api/sso/config');
    },
  },
  {
    name: 'specs-and-host-vars',
    async run(s) {
      await s.fetch('PUT', '/api/specs/web', { body: { spec: SPEC('pr-${PR}'), compose: COMPOSE, description: 'web' } });
      await s.fetch('PUT', '/api/specs/web', { body: { spec: SPEC('pr-${PR}'), compose: COMPOSE } });
      await s.fetch('PUT', '/api/specs/Bad Name', { body: { spec: SPEC('x') } });
      await s.fetch('PUT', '/api/specs/broken', { body: { spec: 'version: 9\n' } });
      await s.fetch('GET', '/api/specs');
      await s.fetch('GET', '/api/specs/web');
      await s.fetch('GET', '/api/specs/nope');
      await s.fetch('PUT', '/api/deployments/pr-7', { body: { specName: 'web', vars: { PR: '7' } } });
      await s.tick();
      await s.fetch('PUT', '/api/deployments/pr-8', { body: { specName: 'web' } });
      await s.fetch('PUT', '/api/deployments/pr-9', { body: { specName: 'nope', vars: { PR: '9' } } });
      await s.fetch('GET', '/api/deployments/pr-7');
      await s.fetch('GET', '/api/deployments/pr-7/source');
      await s.fetch('DELETE', '/api/specs/web');
      await s.fetch('PUT', '/api/host-vars/REGION', { body: { value: 'eu', secret: false } });
      await s.fetch('PUT', '/api/host-vars/DB_PASS', { body: { value: 's3cret', secret: true } });
      await s.fetch('PUT', '/api/host-vars/DB_PASS', { body: { value: 'now-plain', secret: false } });
      await s.fetch('PUT', '/api/host-vars/bad-name', { body: { value: 'x', secret: false } });
      await s.fetch('GET', '/api/host-vars');
      await s.fetch('PUT', '/api/deployments/uses-vars', { body: { spec: 'version: 1\nstack: uses-${vars.REGION}\nenv:\n  X: ${secrets.DB_PASS}\n' } });
      await s.fetch('GET', '/api/deployments/uses-vars');
      await s.fetch('DELETE', '/api/host-vars/REGION');
      await s.fetch('DELETE', '/api/host-vars/REGION');
      await s.fetch('GET', '/api/deployments/uses-vars');
      await s.fetch('DELETE', '/api/deployments/pr-7');
      await s.fetch('DELETE', '/api/specs/web');
    },
  },
  {
    name: 'registries-and-routing',
    async run(s) {
      await s.fetch('PUT', '/api/registries/ghcr.io', { body: { username: 'ci', password: 'ghp_x' } });
      await s.fetch('PUT', '/api/registries/docker.io', { body: { username: 'hub', password: 'p' } });
      await s.fetch('PUT', '/api/registries/not a host', { body: { username: 'x', password: 'y' } });
      await s.fetch('GET', '/api/registries');
      await s.fetch('DELETE', '/api/registries/index.docker.io');
      await s.fetch('DELETE', '/api/registries/nope.example');
      await s.fetch('GET', '/api/registries/a/b');
      await s.fetch('PUT', '/api/routing/extra.yml', { body: { content: 'http:\n  middlewares:\n    compress:\n      compress: {}\n' } });
      await s.fetch('PUT', '/api/routing/bad.yml', { body: { content: 'http: [\n' } });
      await s.fetch('PUT', '/api/routing/../evil.yml', { body: { content: 'x: 1\n' } });
      await s.fetch('PUT', '/api/routing/htpasswd', { body: { content: 'admin:$apr1$x\n' } });
      await s.fetch('GET', '/api/routing');
      await s.fetch('GET', '/api/routing/extra.yml');
      await s.fetch('DELETE', '/api/routing/extra.yml');
      await s.fetch('DELETE', '/api/routing/extra.yml');
    },
  },
  {
    name: 'notifiers',
    async run(s) {
      await s.fetch('POST', '/api/notifiers', { body: { type: 'webhook', name: 'ops', config: { url: 'http://127.0.0.1:1/hook' }, events: ['*'] } });
      await s.tick();
      await s.fetch('POST', '/api/notifiers', { body: { type: 'webhook', name: 'bad', config: { url: 'http://169.254.169.254/' }, events: ['*'] } });
      await s.tick(); // 'bad' IS stored (the harness allows private addresses); 'chat' below must not share its millisecond
      await s.fetch('POST', '/api/notifiers', { body: { type: 'webhook', name: 'typo', config: { url: 'https://example.com/h' }, events: ['stack.reddy'] } });
      await s.fetch('POST', '/api/notifiers', { body: { type: 'pigeon', name: 'x', config: {}, events: ['*'] } });
      await s.fetch('POST', '/api/notifiers', { body: { type: 'slack', name: 'chat', config: { webhookUrl: 'https://hooks.slack.com/services/T00/B00/tok' }, events: ['stack.ready'] } });
      await s.tick();
      await s.fetch('POST', '/api/notifiers', { body: { type: 'discord', name: 'alerts', config: { webhookUrl: 'https://discord.com/api/webhooks/1/t' }, events: ['job.failed'] } });
      await s.fetch('GET', '/api/notifiers');
      await s.fetch('GET', '/api/notifiers/types');
      await s.fetch('PATCH', '/api/notifiers/3', { body: { enabled: false } });
      await s.fetch('PATCH', '/api/notifiers/3', { body: { enabled: 'no' } });
      await s.fetch('POST', '/api/notifiers/1/test');
      await s.until('GET', '/api/notifiers/1/deliveries', (b) => Array.isArray(b.deliveries) && (b.deliveries as unknown[]).length >= 1 && (b.deliveries as Array<{ status: string }>).every((d) => d.status !== 'pending'), 15_000);
      await s.fetch('POST', '/api/notifiers/1/deliveries/1/redeliver');
      await s.fetch('POST', '/api/notifiers/1/deliveries/999/redeliver');
      await s.fetch('GET', '/api/notifiers/99/deliveries');
      await s.fetch('DELETE', '/api/notifiers/2');
      await s.fetch('DELETE', '/api/notifiers/2');
      await s.fetch('GET', '/api/notifiers');
    },
  },
  {
    name: 'swarm',
    shim: SWARM_SHIM,
    async run(s) {
      await s.fetch('GET', '/api/swarm');
      await s.fetch('GET', '/api/swarm/join');
      await s.fetch('GET', '/api/swarm/join?format=script');
      await s.fetch('GET', '/api/swarm/join?format=cloud-config&distro=alpine');
      await s.fetch('GET', '/api/swarm/join?format=pdf');
      await s.fetch('GET', '/api/swarm/join?format=cloud-config&distro=plan9');
      await s.fetch('POST', '/api/swarm/join');
    },
  },
  {
    name: 'share-sleep-wake',
    shim: FIXTURE_SHIM,
    async run(s) {
      await s.fetch('PUT', '/api/deployments/sleepy', { body: { spec: SPEC('sleepy', 'sleep:\n  after: 3d\n  idle: 2h\n'), compose: COMPOSE } });
      await s.tick();
      await s.fetch('PUT', '/api/deployments/shared-db', { body: { spec: 'version: 1\nkind: shared\nstack: shared-db\ncompose:\n  file: docker-compose.yml\n', compose: 'services:\n  db:\n    image: postgres:16\n' } });
      const up = await s.fetch('POST', '/api/deployments/sleepy/up');
      await s.waitJob((up.body.job as { id: string }).id);
      await s.fetch('POST', '/api/deployments/shared-db/sleep');
      await s.fetch('POST', '/api/deployments/shared-db/down');
      const sl = await s.fetch('POST', '/api/deployments/sleepy/sleep');
      await s.waitJob((sl.body.job as { id: string }).id);
      await s.fetch('GET', '/api/deployments/sleepy');
      await s.fetch('GET', '/api/deployments');
      await s.fetch('GET', '/', { headers: { host: 'app-sleepy.preview.example.com', authorization: '' } });
      await s.fetch('GET', '/api/health', { headers: { host: 'app-sleepy.preview.example.com', authorization: '' } });
      await s.until('GET', '/api/jobs', (b) => Array.isArray(b.jobs) && (b.jobs as Array<{ action: string; state: string }>).some((j) => j.action === 'wake' && j.state !== 'running'));
      await s.fetch('GET', '/api/deployments/sleepy');
      await s.fetch('GET', '/', { headers: { host: 'control.preview.example.com', authorization: '' } });
      await s.fetch('POST', '/api/deployments/sleepy/share', { body: { views: ['logs'], ttl: '2h' } });
      await s.fetch('POST', '/api/deployments/sleepy/share', { body: { views: ['admin'] } });
      await s.fetch('POST', '/api/deployments/sleepy/share', { body: { ttl: '99d' } });
      const share = await s.fetch('POST', '/api/deployments/sleepy/share', { body: {} });
      const token = share.body.token as string;
      await s.fetch('GET', `/api/auth/me?token=${token}`, { headers: { authorization: '' } });
      await s.fetch('GET', `/api/deployments/sleepy?token=${token}`, { headers: { authorization: '' } });
      await s.fetch('GET', `/api/deployments/shared-db?token=${token}`, { headers: { authorization: '' } });
      await s.fetch('POST', `/api/deployments/sleepy/up?token=${token}`, { headers: { authorization: '' } });
      await s.fetch('GET', `/api/deployments/sleepy/logs?tail=5&token=${token}&PR=8`, { headers: { authorization: '' } });
      await s.fetch('GET', `/api/deployments?token=${token}`, { headers: { authorization: '' } });
    },
  },
];

