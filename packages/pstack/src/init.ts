/**
 * `pstack init` — stand up the CONTROL stack on a host.
 *
 * ── WHY THIS LIVES IN THE CLI AND NOT IN THE API ──────────────────────────────────────────────
 *
 *     host (systemd / CLI)
 *       └─ pstack init ──▶ CONTROL stack: traefik + pstack (API + UI)
 *                                           │ manages
 *                                           ▼
 *                               shared + isolated deployments
 *
 * The running API manages every OTHER deployment. It must never manage the stack it is running
 * in, because `up` on that project recreates the pstack container — killing the process halfway
 * through the operation it was performing. The request never returns, the job transcript is lost
 * with the process (job history is in-memory, invariant 10), and if the new image is broken the
 * host is left with no control plane and no remote way to fix it: the only thing that could have
 * repaired it was the thing that just died. So self-management is not a feature with a caveat,
 * it is a way to brick a host from a browser tab.
 *
 * The host keeps that power. `init` runs over SSH, from systemd, or from CI-with-a-key — always
 * from OUTSIDE the containers it is recreating, which is the only place a failed upgrade is
 * recoverable.
 *
 * Everything here is idempotent: re-running `init` is the supported way to change the domain,
 * rotate the token, or move to a new image.
 */

import { chmod, mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { shq } from './compose.ts';
import type { Runner } from './exec.ts';
import { escapeHostRegexp } from './subdomains.ts';
import { SWARM_PORTS } from './swarm.ts';
import type { Orchestrator } from './spec.ts';
// Embedded at BUILD time, not read from disk at runtime: the published package is a single bundled
// file, so `new URL('../templates/…', import.meta.url)` would point at a path that does not ship.
import CONTROL_TEMPLATE from '../templates/control/docker-compose.yml' with { type: 'text' };

/** Compose project name of the control stack. Fixed: there is exactly one per host. */
/**
 * The control stack's compose project name. Exported because the API reads it to SHOW the control
 * stack's status — never to act on it. Keep it stable: it is how an operator finds the stack with
 * `docker compose -p pstack-control …`.
 */
export const CONTROL_PROJECT = 'pstack-control';

/**
 * The two external Docker networks, matching `docs/bootstrap.md` §1 and the per-PR compose
 * contract in §5. They answer different questions — `preview-ingress` is "who may Traefik route
 * to", `preview-shared` is "who may reach shared infrastructure" — so a per-PR service that needs
 * the database but must not be publicly routable joins only the second.
 *
 * These names are a CONTRACT with every per-PR compose file, which must declare both as
 * `external: true`. Declare a different name on one side and Compose silently creates
 * `pr-123_preview-ingress` instead: the container comes up healthy and is unreachable (§10).
 * Renaming them here is therefore a breaking change to every spec on the host.
 */
const NET_INGRESS = 'preview-ingress';
const NET_SHARED = 'preview-shared';

const SOCKET = '/var/run/docker.sock';

/**
 * The image the control stack runs. Not an `init` option because it is a property of the
 * *installation* (what you built or pulled), not of the host you are configuring; override with
 * PSTACK_IMAGE when you publish the image somewhere.
 */
const defaultImage = () => process.env.PSTACK_IMAGE ?? 'pstack:local';

/**
 * lego's DNS-01 credential variable, per provider — the name Traefik expects the token under.
 *
 * ONLY the providers verified in `docs/bootstrap.md` §8 are listed. A wrong variable name fails
 * as "propagation timeout", which sends you debugging DNS instead of a typo, so an unknown
 * provider gets a CHANGEME line pointing at lego's list rather than a guess.
 *
 * `null` means the provider is *tokenless*: AWS satisfies the credential chain from an instance
 * profile and GCP from the VM's attached service account, so there is nothing to write, nothing
 * on disk, and nothing to rotate. Prefer those where you have the choice.
 */
export const DNS_TOKEN_VAR: Record<string, string | null> = {
  cloudflare: 'CF_DNS_API_TOKEN', // a single token with Zone:Read + DNS:Edit
  hetzner: 'HETZNER_API_TOKEN',
  route53: null, // IAM instance profile
  gcloud: null, // Application Default Credentials
};

export type InitOptions = {
  /** Where the control plane keeps its state — the same path the API reads as PSTACK_DATA. */
  dataDir: string;
  /** Apex of the preview domain, e.g. `preview.example.com`. The UI lands on `pstack.<domain>`. */
  domain: string;
  /** ACME registration address. Let's Encrypt mails expiry warnings here. */
  acmeEmail: string;
  /**
   * How Let's Encrypt verifies the domain.
   *
   *   'http01'  DEFAULT. Zero credentials — Traefik answers a challenge on port 80. Works the
   *             moment DNS points at the box. CANNOT issue wildcards, so every hostname gets its
   *             own certificate, and Let's Encrypt allows only ~50 new certificates per registered
   *             domain per week. At 3 surfaces per PR that is ~16 PRs/week. It also cannot cover a
   *             hostname before its container exists, so a not-yet-deployed preview URL has no cert.
   *   'dns01'   One wildcard covers every present and future `*.<domain>` host: no per-PR issuance,
   *             no rate-limit ceiling, and a URL is valid before the stack is deployed. Costs a DNS
   *             API credential to obtain, store and rotate.
   *
   * Start on http01; move to dns01 when PR volume or pre-deploy URLs demand it.
   */
  challenge: 'http01' | 'dns01';
  /**
   * Which web UI `control.<domain>` serves.
   *
   *   'basic'     DEFAULT. The UI embedded in the API bundle — no extra container, nothing else to
   *               build or pull. Enough to submit a deployment and watch a job.
   *   'advanced'  The standalone SPA (@samyx/preview-stacks-ui) as its OWN container, which the
   *               control stack gains a service for. Richer, but one more image to build and keep
   *               current, so it is opt-in rather than the default.
   *
   * Either way the API keeps serving the basic UI on `api.<domain>`, so a broken advanced image
   * never leaves the host with no usable interface.
   */
  ui: 'basic' | 'advanced';
  /**
   * How previews are deployed on this host.
   *
   *   'swarm'    DEFAULT for a new host. This daemon becomes a one-node swarm manager; previews deploy
   *              as swarm stacks; workers can join later from the swarm panel. The control stack
   *              itself stays a plain compose project on the manager (it must never depend on the
   *              orchestrator it manages), and the two external networks become attachable overlays
   *              so its containers reach tasks on any node.
   *   'compose'  What every host before 0.26.0 ran. `pstack upgrade` keeps it: switching an existing
   *              host means recreating its networks, which needs every preview torn down first.
   */
  orchestrator: Orchestrator;
  /** lego DNS-01 provider code, e.g. `cloudflare`. Required for, and only used by, `dns01`. */
  dnsProvider?: string;
  /**
   * The DNS-01 API token, and ONLY that — it is written to `dns.env` for Traefik and never
   * reused as the API's bearer token. Two different secrets, two different blast radii: this one
   * can edit a DNS zone, PSTACK_TOKEN can start privileged containers. Omit it for the tokenless
   * providers (route53, gcloud), which authenticate from the instance's own identity.
   */
  token?: string;
  /** Change nothing — print what would happen. Must match the flag the runner was built with. */
  dryRun: boolean;
  /** From `createRunner()`, so `--dry-run` behaves exactly as it does everywhere else. */
  runner: Runner;
};

/** 24 random bytes, hex. Hex on purpose: `$` in a `.env` value is expanded by Compose. */
function randomToken(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(24)), (b) =>
    b.toString(16).padStart(2, '0'),
  ).join('');
}

export async function init(opts: InitOptions): Promise<void> {
  const { dataDir, domain, acmeEmail, dnsProvider, challenge, ui, orchestrator, dryRun, runner } = opts;
  const image = defaultImage();
  const uiImage = process.env.PSTACK_UI_IMAGE ?? 'pstack-ui:local';
  const controlDir = join(dataDir, 'control');
  const composePath = join(controlDir, 'docker-compose.yml');

  // The API's bearer token — deliberately NOT `opts.token`, which is the DNS credential. Reusing
  // one secret for both would give whoever can edit a DNS zone the ability to start privileged
  // containers on this host.
  //
  // Generated when unset rather than left empty: the control stack binds 0.0.0.0 so Traefik can
  // reach it, and `serve` hard-exits (3) on a non-loopback bind with no PSTACK_TOKEN. An
  // unauthenticated API holding a read-write Docker socket must not be reachable, ever. Supply
  // your own through the environment (`PSTACK_TOKEN=… pstack init`) to keep an existing one.
  const pstackToken = process.env.PSTACK_TOKEN || randomToken();
  const generated = !process.env.PSTACK_TOKEN;

  // ── 0. Preconditions ────────────────────────────────────────────────────────────────────────
  // Same shape and same reason as a spec's `requires:` — fail immediately, by name, before
  // anything is created, rather than deep inside a compose error nobody can read.
  // NOTE: under --dry-run these are skipped and report ok, like every other command. A green
  // dry-run proves ordering, never absence (AGENTS.md).
  for (const req of [
    {
      name: 'docker socket',
      assert: `test -S ${SOCKET}`,
      hint: `no Docker socket at ${SOCKET} — install Docker Engine first (docs/bootstrap.md §4).`,
    },
    {
      name: 'compose plugin',
      assert: 'docker compose version >/dev/null 2>&1',
      hint: 'the Compose v2 plugin is missing — install docker-compose-plugin.',
    },
    {
      name: 'control image',
      assert: `docker image inspect ${shq(image)} >/dev/null 2>&1`,
      // Point at the command that needs nothing but this install. Telling an operator to clone
      // the source is what made a global install a dead end in the first place.
      hint:
        `image ${image} not found — build it from this install: \`pstack build-image\`` +
        `${image === 'pstack:local' ? '' : ` --tag ${image}`}` +
        ` (or pull it and pass PSTACK_IMAGE=<registry>/<image>)`,
    },
    // Only when the advanced UI is selected. Compose does not fail-fast on a missing image: it
    // tries to PULL `pstack-ui:local`, gets "pull access denied", and takes the WHOLE control stack
    // down with it — Traefik included. So an optional UI turned into a dead host. Check it here,
    // where the message can name the command that fixes it.
    ...(ui === 'advanced'
      ? [
          {
            name: 'advanced UI image',
            assert: `docker image inspect ${shq(uiImage)} >/dev/null 2>&1`,
            hint:
              `image ${uiImage} not found — build it with \`pstack build-image --ui\`` +
              `${uiImage === 'pstack-ui:local' ? '' : ` --tag ${uiImage}`}, ` +
              `or re-run without --ui advanced to use the basic UI embedded in the API.`,
          },
        ]
      : []),
  ]) {
    const r = await runner.run(req.assert, { label: `requires ${req.name}` });
    if (!r.ok) throw new Error(`${req.name}: ${req.hint}`);
  }

  // ── 1. State directories ────────────────────────────────────────────────────────────────────
  // `deployments/` is the Registry's root (it joins dataDir + 'deployments'); `traefik-dynamic/`
  // is the file provider's watched directory, created empty so the mount does not fail.
  await ensureDir(join(dataDir, 'deployments'), dryRun);
  await ensureDir(join(controlDir, 'traefik-dynamic'), dryRun);
  // DOCKER_CONFIG for the API's docker client. 0700 because config.json holds registry credentials as
  // reversible base64 — see src/registries.ts.
  await ensureDir(join(controlDir, 'docker'), dryRun, 0o700);
  // The SQLite home (accounts, sessions, tokens). 0700 for the same reason as the docker config:
  // WAL siblings carry live data, so the directory is the permission boundary, not the file.
  await ensureDir(join(dataDir, 'db'), dryRun, 0o700);

  // ── 1b. Swarm ───────────────────────────────────────────────────────────────────────────────
  // `swarm init` on a node that is already a manager fails, so it is guarded by the state docker
  // reports — this is the idempotent path. Leaving a swarm is never done here: that is a decision
  // with workers attached to it, and it belongs to an operator at a shell.
  if (orchestrator === 'swarm') {
    const state = await runner.run(`docker info --format '{{.Swarm.LocalNodeState}}'`, { label: 'swarm state' });
    if (state.stdout.trim() !== 'active') {
      const r = await runner.run('docker swarm init', { label: 'swarm init' });
      if (!r.ok) {
        throw new Error(
          `docker swarm init failed:\n${indent(r.stderr || r.stdout)}\n` +
            `  A host with several addresses needs --advertise-addr; run \`docker swarm init --advertise-addr <ip>\` ` +
            `once by hand, then re-run pstack init.`,
        );
      }
    }
  }

  // ── 2. External networks ────────────────────────────────────────────────────────────────────
  // `network create` exits non-zero when the network exists, and this must be re-runnable, so the
  // failure is swallowed — this is the idempotent path, not an ignored error.
  //
  // Under swarm they must be OVERLAY networks (a task on a worker is not on this host's bridge) and
  // `--attachable`, or the control stack's plain containers could not join them. A network that
  // exists with the other driver is the one thing that cannot be fixed quietly: it has to be removed,
  // which is only possible while nothing is attached — so the control stack is taken down for it,
  // and a preview still attached is a hard stop naming it.
  const driver = orchestrator === 'swarm' ? 'overlay' : 'bridge';
  const createArgs = orchestrator === 'swarm' ? '-d overlay --attachable ' : '';
  for (const net of [NET_INGRESS, NET_SHARED]) {
    const have = await runner.run(`docker network inspect -f '{{.Driver}}' ${net} 2>/dev/null`, { label: `network ${net} driver` });
    const current = have.ok ? have.stdout.trim() : '';
    // Only a driver docker could have meant. Anything else is "could not tell", and a network that
    // cannot be read is not one to remove.
    if ((current === 'bridge' || current === 'overlay') && current !== driver) {
      const attached = await runner.run(
        `docker network inspect -f '{{range .Containers}}{{.Name}} {{end}}' ${net}`,
        { label: `network ${net} members` },
      );
      const names = attached.stdout.trim().split(/\s+/).filter(Boolean);
      const foreign = names.filter((n) => !n.startsWith(`${CONTROL_PROJECT}-`));
      if (foreign.length > 0) {
        throw new Error(
          `network ${net} is a ${current} network and must become ${driver} for --orchestrator ${orchestrator}, ` +
            `but these containers are attached to it: ${foreign.join(', ')}.\n` +
            `  Tear those previews down (pstack down / the API), then re-run init. Or keep the host as it ` +
            `is with --orchestrator ${current === 'overlay' ? 'swarm' : 'compose'}.`,
        );
      }
      // Only the control stack is on it: take that down (it is recreated below anyway), swap the network.
      await runner.run(`docker compose -p ${CONTROL_PROJECT} down 2>/dev/null || true`, { label: 'control stack down (network swap)' });
      const rm = await runner.run(`docker network rm ${net}`, { label: `network ${net} rm` });
      if (!rm.ok) throw new Error(`could not replace network ${net}:\n${indent(rm.stderr || rm.stdout)}`);
    }
    await runner.run(`docker network create ${createArgs}${net} 2>/dev/null || true`, { label: `network ${net}` });
  }

  // ── 3. Configuration ────────────────────────────────────────────────────────────────────────
  // The template's `${...}` placeholders are *Compose's* own interpolation, resolved from `.env` at
  // `up` time — never substituted here (invariant 6: exactly one interpolator, in spec.ts).
  //
  // The two `#__MARKER__` lines are different: Compose cannot conditionally include CLI arguments,
  // and the ACME challenge changes both Traefik's flags and the router's TLS labels. So init
  // renders those two blocks and leaves everything else byte-for-byte.
  // Function replacements, not strings: `String.replace` reads `$$` in a string replacement as one
  // `$`, and the wake router's rule ends in `$$` precisely so compose hands Traefik a literal `$`.
  const template = CONTROL_TEMPLATE
    .replace('      #__ACME_CHALLENGE__', () => acmeChallengeArgs(challenge, dnsProvider))
    .replace('      #__ACME_ROUTER_TLS__', () => acmeRouterLabels(challenge))
    .replace('      #__CONTROL_UI_SERVICE__', () => controlUiService(ui))
    .replace('      #__SWARM_PROVIDER__', () => swarmProviderArgs(orchestrator))
    .replace('      #__WAKE_ROUTER__', () => wakeRouterLabels(domain))
    .replace('#__ADVANCED_UI_SERVICE__', () => advancedUiService(ui));
  await write(composePath, template, 0o644, dryRun);

  await write(
    join(controlDir, '.env'),
    envFile({ dataDir, domain, acmeEmail, dnsProvider: dnsProvider ?? '', image, pstackToken, ui, uiImage, orchestrator }),
    // 0600: this file holds PSTACK_TOKEN, and that token drives an API with a read-write Docker
    // socket — i.e. root on this host. Anyone who can read it owns the box.
    0o600,
    dryRun,
  );

  // Only meaningful for dns01; written either way so switching modes needs no extra step.
  await write(join(controlDir, 'dns.env'), dnsEnvFile(dnsProvider ?? '', opts.token), 0o600, dryRun);

  // ── 4. Bring it up ──────────────────────────────────────────────────────────────────────────
  const upCmd = `docker compose -p ${CONTROL_PROJECT} -f ${shq(composePath)} up -d --remove-orphans`;
  const up = await runner.run(upCmd, { label: 'control stack up' });
  if (!up.ok) {
    throw new Error(`control stack failed to start:\n${indent(up.stderr || up.stdout)}`);
  }

  // ── 5. Prove it ─────────────────────────────────────────────────────────────────────────────
  // `up -d` exits 0 as soon as the containers are *created*. A container that crash-loops still
  // gets you a zero here, so `init` would print success over a dead control plane. The image ships
  // a HEALTHCHECK against /api/health; wait for it and report what actually happened.
  if (!dryRun) {
    const health = await waitHealthy(runner);
    if (health !== 'healthy') {
      throw new Error(
        `control stack started but the API never became healthy (last state: ${health}).\n` +
          `  docker compose -p ${CONTROL_PROJECT} logs pstack\n` +
          `  docker compose -p ${CONTROL_PROJECT} ps`,
      );
    }
  }

  // ── 6. What to do next ──────────────────────────────────────────────────────────────────────
  // control.<domain>, not the long-gone pstack.<domain> — the hostnames split when the API and UI
  // got their own routers.
  const uiUrl = `https://control.${domain}`;
  const apiUrl = `https://api.${domain}`;
  console.log(
    [
      '',
      `control stack up  (project ${CONTROL_PROJECT}${dryRun ? ', dry-run' : ''})`,
      `  config    ${composePath}`,
      `  registry  ${join(dataDir, 'deployments')}`,
      `  networks  ${NET_INGRESS}, ${NET_SHARED}   (${driver}; declare both as \`external: true\` in every per-PR compose file)`,
      `  previews  ${orchestrator === 'swarm' ? 'docker stack deploy on this swarm (one manager; add workers from the Swarm page)' : 'docker compose on this host'}`,
      ...(orchestrator === 'swarm'
        ? [`            workers need ${SWARM_PORTS.map((p) => p.port).join(', ')} open to and from this host`]
        : []),
      '',
      'next:',
      `  1. UI          ${uiUrl}`,
      `     DNS must already answer for ${domain} and *.${domain}; the wildcard certificate is`,
      `     requested on first request and can take a minute (docs/bootstrap.md §10 if it never arrives).`,
      `  2. Health      curl -sf ${apiUrl}/api/health`,
      `  3. Submit      curl -X PUT ${apiUrl}/api/deployments/<id> \\`,
      `                   -H "Authorization: Bearer $PSTACK_TOKEN" \\`,
      `                   -H 'content-type: application/json' \\`,
      `                   -d '{"spec": "<preview.yml>", "compose": "<docker-compose.yml>"}'`,
      `                 then POST /api/deployments/<id>/up. A spec with \`stack: pr-\${PR}\` needs`,
      `                 its variables on every call (…?PR=7) — and the SAME ones on \`down\`, or`,
      `                 teardown targets a different stack than deploy created. Full route list:`,
      `                 the header of src/api.ts. Or just use the UI.`,
      `  4. Upgrade     pstack upgrade  — reads this .env so the token, domain and challenge`,
      `                 mode all survive. Never from inside the control stack; SSH in.`,
      `  5. Rotate      PSTACK_TOKEN=<new> pstack init …  — re-running rewrites .env and recreates`,
      `                 the container. Omit it entirely to be handed a fresh one. The only copy`,
      `                 lives in ${join(controlDir, '.env')} (0600); the DNS credential is a`,
      `                 separate secret in dns.env and is not affected.`,
      '',
      generated
        ? `  PSTACK_TOKEN=${pstackToken}\n  ^ generated — this is the ONLY time it is printed. Store it now.`
        : `  PSTACK_TOKEN: taken from the environment (stored 0600 in ${join(controlDir, '.env')})`,
      '',
    ].join('\n'),
  );
}

/**
 * Poll the pstack container's HEALTHCHECK. Asking Compose for the id rather than assuming
 * `<project>-<service>-1` keeps this correct if the container is renamed or scaled.
 *
 * Bounded at ~60s: the health check itself has a start period, and a control plane that needs
 * longer than a minute to answer its own liveness route has something to say in its logs.
 */
async function waitHealthy(runner: Runner): Promise<string> {
  const probe =
    `cid=$(docker compose -p ${CONTROL_PROJECT} ps -q pstack) && [ -n "$cid" ] && ` +
    `docker inspect -f '{{.State.Health.Status}}' "$cid"`;
  let last = 'unknown';
  for (let i = 0; i < 30; i++) {
    const r = await runner.run(probe, { label: 'health' });
    last = r.stdout.trim() || last;
    if (last === 'healthy') return last;
    // `unhealthy` is only reported after the start period AND the retry budget, so it is a verdict,
    // not a transient — waiting longer just delays the same error.
    if (last === 'unhealthy') return last;
    await Bun.sleep(2000);
  }
  return last;
}

/** The values every `${...}` in the compose template reads. Compose loads this from `.env`. */
function envFile(v: {
  ui?: 'basic' | 'advanced';
  uiImage?: string;
  orchestrator: Orchestrator;
  dataDir: string;
  domain: string;
  acmeEmail: string;
  dnsProvider: string;
  image: string;
  pstackToken: string;
}): string {
  return [
    '# Written by `pstack init`. Compose reads this file from the project directory at `up` time.',
    '# Re-run `pstack init` to change anything here rather than editing by hand — the two must agree.',
    '#',
    '# Compose expands `${...}` inside these values. A literal `$` must be written `$$`.',
    '',
    `DATA_DIR=${v.dataDir}`,
    `DOMAIN=${v.domain}`,
    // Only meaningful with --ui advanced; harmless otherwise, and having it present means
    // switching modes is a re-run of `init` rather than an .env edit.
    `PSTACK_UI_IMAGE=${v.uiImage ?? 'pstack-ui:local'}`,
    `ACME_EMAIL=${v.acmeEmail}`,
    `DNS_PROVIDER=${v.dnsProvider}`,
    `PSTACK_IMAGE=${v.image}`,
    '# How previews deploy: swarm (docker stack deploy) or compose. `pstack upgrade` keeps it.',
    `PSTACK_ORCHESTRATOR=${v.orchestrator}`,
    '',
    '# Bearer token for the API. Mutating routes require it; GETs do not, so ingress auth is still',
    '# the only gate on job transcripts and the stack list (docs/bootstrap.md §9).',
    `PSTACK_TOKEN=${v.pstackToken}`,
    '',
  ].join('\n');
}

/**
 * The DNS-01 credential, in its own file because Compose cannot build an environment key from a
 * variable — and every provider names its token differently. `env_file:` is the native answer:
 * arbitrary keys, one file, no interpolation of the key itself.
 */
/**
 * Traefik's ACME challenge flags for the chosen mode, at the template's indentation.
 *
 * HTTP-01 needs no credential: Traefik answers on the `web` entrypoint over port 80. The
 * web→websecure redirect does NOT break it — Traefik installs an internal ACME router at maximum
 * priority that bypasses the redirect for `/.well-known/acme-challenge/`. Port 80 must be reachable
 * from the internet, which is the one hard requirement.
 */
function acmeChallengeArgs(challenge: 'http01' | 'dns01', provider: string | undefined): string {
  if (challenge === 'http01') {
    return [
      '      - --certificatesresolvers.le.acme.httpchallenge=true',
      '      # Must be the :80 entrypoint, and :80 must be reachable from the internet.',
      '      - --certificatesresolvers.le.acme.httpchallenge.entrypoint=web',
    ].join('\n');
  }
  return [
    '      - --certificatesresolvers.le.acme.dnschallenge=true',
    `      - --certificatesresolvers.le.acme.dnschallenge.provider=\${DNS_PROVIDER}`,
    '      # Ask the authoritative resolvers directly so a stale local cache cannot fail the',
    '      # propagation check.',
    '      - --certificatesresolvers.le.acme.dnschallenge.resolvers=1.1.1.1:53',
    '      - --certificatesresolvers.le.acme.dnschallenge.propagation.delaybeforechecks=30s',
  ].join('\n') + (provider ? '' : '');
}

/**
 * The TLS labels on the control router.
 *
 * dns01 requests ONE wildcard from a single always-on router; every other router (including every
 * per-PR one) sets `tls=true` alone and inherits it by SNI. Adding `certresolver` to another router
 * makes Traefik order a SEPARATE certificate for it — the rate-limit trap.
 *
 * http01 cannot do wildcards at all, so each router resolves its own certificate on first request.
 */
function acmeRouterLabels(challenge: 'http01' | 'dns01'): string {
  if (challenge === 'http01') {
    return [
      '      # HTTP-01 cannot issue wildcards, so each hostname resolves its own certificate on its',
      '      # first HTTPS request. Per-PR routers therefore need `tls.certresolver=le` too — unlike',
      '      # the dns01 setup, where they must NOT have it.',
      '      - traefik.http.routers.pstack-ui.tls=true',
      '      - traefik.http.routers.pstack-ui.tls.certresolver=le',
      '      - traefik.http.routers.pstack-api.tls=true',
      '      - traefik.http.routers.pstack-api.tls.certresolver=le',
    ].join('\n');
  }
  return [
    '      # THE ONE ROUTER THAT REQUESTS THE WILDCARD. Every other router — including every per-PR',
    '      # service — sets `tls=true` and NOTHING else, inheriting this certificate by SNI. Adding',
    '      # `certresolver` to a per-PR router orders a separate certificate and burns the',
    '      # ~50-per-week limit. The wildcard matches ONE label: `backend-pr-1.${DOMAIN}` is covered,',
    '      # `backend.pr-1.${DOMAIN}` is not — flatten per-PR hostnames with dashes.',
    '      - traefik.http.routers.pstack-ui.tls=true',
    '      - traefik.http.routers.pstack-ui.tls.certresolver=le',
    '      - traefik.http.routers.pstack-ui.tls.domains[0].main=${DOMAIN}',
    '      - traefik.http.routers.pstack-ui.tls.domains[0].sans=*.${DOMAIN}',
    '      - traefik.http.routers.pstack-api.tls=true',
  ].join('\n');
}

/**
 * Traefik's swarm provider, in swarm mode only. Both providers run: docker for the control stack's
 * own containers (and any compose-mode preview), swarm for the stacks. `exposedbydefault=false` on
 * both for the same reason — routing is opt-in.
 */
function swarmProviderArgs(orchestrator: Orchestrator): string {
  if (orchestrator !== 'swarm') return '      # (compose mode: no swarm provider)';
  return [
    '      - --providers.swarm=true',
    '      - --providers.swarm.exposedbydefault=false',
    '      - --providers.swarm.network=preview-ingress',
  ].join('\n');
}

/**
 * The catch-all router for wake-on-call. A Go regexp over one label under the domain; `$$` is how a
 * literal `$` survives compose's interpolation of this file, and the dots are escaped here (the
 * template cannot, it only knows `${DOMAIN}`).
 */
function wakeRouterLabels(domain: string): string {
  const re = escapeHostRegexp(domain);
  return [
    `      - traefik.http.routers.pstack-wake.rule=HostRegexp(\`^[a-z0-9-]+\\.${re}$$\`)`,
    '      - traefik.http.routers.pstack-wake.priority=1',
    '      - traefik.http.routers.pstack-wake.entrypoints=websecure',
    '      - traefik.http.routers.pstack-wake.tls=true',
    '      - traefik.http.routers.pstack-wake.service=pstack',
  ].join('\n');
}

/**
 * Which Traefik service `control.<domain>` routes to.
 *
 * The ROUTER stays on the pstack container in both modes so the TLS labels render identically;
 * only its target changes. Traefik happily lets a router declared on one container reference a
 * loadbalancer service declared on another, which keeps this a one-line swap.
 */
function controlUiService(ui: 'basic' | 'advanced'): string {
  return ui === 'advanced'
    ? [
        '      # Advanced UI selected: control.<domain> serves the SPA container below. The API',
        '      # still serves the basic UI on api.<domain>, so a broken UI image never leaves this',
        '      # host without an interface.',
        '      - traefik.http.routers.pstack-ui.service=advanced-ui',
      ].join('\n')
    : '      - traefik.http.routers.pstack-ui.service=pstack';
}

/** The optional advanced-UI container. Omitted entirely in basic mode — not merely disabled. */
function advancedUiService(ui: 'basic' | 'advanced'): string {
  if (ui !== 'advanced') return '';
  return [
    '',
    '  # The opt-in advanced UI (`pstack init --ui advanced`). A static SPA behind nginx, which',
    '  # proxies /api/ back to the pstack container so the browser stays same-origin and needs no',
    '  # CORS. Build it with `pstack build-image --ui`.',
    '  advanced-ui:',
    '    image: ${PSTACK_UI_IMAGE}',
    '    restart: unless-stopped',
    '    mem_limit: 128m',
    '    depends_on: [pstack]',
    '    networks: [preview-ingress]',
    '    labels:',
    '      - traefik.enable=true',
    '      - traefik.docker.network=preview-ingress',
    '      # No router here: control.<domain> is declared on the pstack container (so the TLS',
    '      # labels stay in one place) and points at this service by name.',
    '      - traefik.http.services.advanced-ui.loadbalancer.server.port=80',
    '',
  ].join('\n');
}

function dnsEnvFile(provider: string, token: string | undefined): string {
  const head = [
    '# DNS-01 credential for Traefik (lego). Written by `pstack init`, mode 0600.',
    '# Every lego variable also accepts a `_FILE` suffix if you would rather mount the secret.',
    '# Provider list + exact variable names: https://go-acme.github.io/lego/dns/',
    '',
  ];
  const known = provider in DNS_TOKEN_VAR;
  const varName = DNS_TOKEN_VAR[provider];

  if (known && varName === null) {
    return [
      ...head,
      `# provider "${provider}" is tokenless: it authenticates from the instance's own identity`,
      '# (AWS instance profile / GCP attached service account). Nothing to store, nothing to rotate.',
      '',
    ].join('\n');
  }
  if (!known) {
    return [
      ...head,
      `# provider "${provider}" is not one of the names verified in docs/bootstrap.md §8, so the`,
      '# variable it expects is NOT guessed here — a wrong name surfaces as an ACME "propagation',
      '# timeout", which sends you debugging DNS instead of this line.',
      '#',
      '# Look it up in lego\'s provider list above and replace CHANGEME_VARIABLE_NAME:',
      `CHANGEME_VARIABLE_NAME=${token ?? ''}`,
      '',
    ].join('\n');
  }
  return [...head, `${varName}=${token ?? ''}`, ''].join('\n');
}

/** mkdir -p, or say what it would have made. A dry run that creates directories is not a dry run. */
async function ensureDir(path: string, dryRun: boolean, mode?: number): Promise<void> {
  if (dryRun) {
    console.log(`  [dry-run] mkdir -p ${path}`);
    return;
  }
  await mkdir(path, { recursive: true });
  // Applied separately: `mkdir`'s mode is masked by the process umask, so a 0700 request can land as
  // 0755 — and this directory holds registry credentials.
  if (mode !== undefined) await chmod(path, mode).catch(() => {});
}

/**
 * Write a file at an exact mode.
 *
 * `chmod` is separate and unconditional because `writeFile`'s `mode` applies only when the file is
 * CREATED. `init` is meant to be re-run, so on the second pass a `.env` that someone loosened to
 * 0644 would silently stay 0644 — the one file where that matters most.
 */
async function write(path: string, body: string, mode: number, dryRun: boolean): Promise<void> {
  if (dryRun) {
    console.log(`  [dry-run] write ${path} (${body.length} bytes, mode ${mode.toString(8)})`);
    return;
  }
  await writeFile(path, body, 'utf8');
  await chmod(path, mode);
}

function indent(s: string): string {
  return s
    .trimEnd()
    .split('\n')
    .map((l) => `    | ${l}`)
    .join('\n');
}
