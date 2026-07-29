/**
 * Wildcard subdomain routing for a per-PR surface.
 *
 * THE GOAL. Given profile `backend` on stack `pr-123`, make anything under
 * `backend-pr-123.<domain>` reach the same place `backend-pr-123.<domain>` reaches —
 * `api.backend-pr-123.<domain>`, `tenant-a.backend-pr-123.<domain>`, and (opt-in) deeper still.
 * Apps that route by subdomain — per-tenant hosts, per-branch previews inside a preview — need it,
 * and hard-coding one host per tenant into a per-PR compose file does not scale.
 *
 * HOW IT PLUGS IN, AND WHY THIS WAY. pstack does not own your per-PR Traefik labels; your compose
 * file does (see docs/control-plane.md § Hostnames). So this module does not write labels, generate
 * an overlay, or touch Traefik's dynamic directory. It computes the one part that is genuinely
 * fiddly — a correctly escaped, correctly anchored Go regexp — and exposes it as an environment
 * variable that `docker compose` interpolates into a label you write:
 *
 *     labels:
 *       - traefik.http.routers.backend-wild.rule=${PSTACK_WILD_BACKEND}
 *       - traefik.http.routers.backend-wild.priority=2
 *       - traefik.http.routers.backend-wild.service=backend
 *
 * Three alternatives were rejected:
 *
 *   - **Merging labels via a generated overlay.** Compose's merge semantics for list-form `labels`
 *     decide whether your own routers survive that merge, and getting it wrong deletes them
 *     silently. Not a risk worth taking for a convenience feature.
 *   - **Writing Traefik file-provider config.** Would need the API container mounted into
 *     `traefik-dynamic/` read-write, which means every existing host must re-run `pstack init` —
 *     and one malformed file in that directory takes down routing for *everything* on the box, by
 *     Traefik's own documented behaviour.
 *   - **A label-carrying sidecar container per PR.** A whole container to hold three strings.
 *
 * The env-var seam needs none of that: no new mount, no re-`init`, no capability added to the API,
 * and nothing pstack writes can break another deployment's routing.
 *
 * "UNLESS IT'S HARDCODED" IS FREE. Traefik's default router priority is the rule's length, so an
 * exact `Host(`admin.backend-pr-123.example.com`)` scores in the dozens and beats a wildcard router
 * pinned at `priority=2`. Pin it explicitly anyway: a *generated* number that happens to be lower
 * is not a guarantee, and 2 also clears the `preview-fallback` catch-all at priority 1.
 *
 * ── THE CEILINGS. Read these before promising anyone arbitrary depth. ────────────────────────────
 *
 * Routing is the easy third of the problem. The other two are outside pstack:
 *
 *   1. **DNS.** A wildcard record matches exactly ONE label. `*.example.com` answers for
 *      `a.example.com` and NOT for `a.b.example.com`, and `*.*.example.com` is not a valid record.
 *      So `depth: any` needs a resolver that answers for arbitrary depth — a self-hosted
 *      authoritative server, or a provider with a non-standard extension. Most managed DNS cannot.
 *   2. **TLS.** A certificate wildcard also matches exactly one label, so
 *      `*.<domain>` does NOT cover `x.backend-pr-123.<domain>`. Serving that over HTTPS needs a
 *      certificate for `*.backend-pr-123.<domain>` — which means **DNS-01** (HTTP-01 cannot issue
 *      wildcards at all) and **one certificate per PR**, against Let's Encrypt's ~50
 *      certificates/registered-domain/week. And **no certificate can cover `depth: any`**:
 *      `*.*.host` is not a legal SAN. Arbitrary depth is therefore HTTP-only, permanently.
 *
 * Hence `depth` defaults to `one`: the depth DNS and TLS can actually deliver. `any` is available
 * because the request was explicit about wanting it, and is honest about being routing-only.
 */

import { SpecError } from './errors.ts';

/** How many labels the wildcard spans. `one` is what DNS and TLS can cover; `any` is HTTP-only. */
export type SubdomainDepth = 'one' | 'any';

export type SubdomainRoute = {
  /** The compose profile this belongs to. Also the default hostname prefix. */
  profile: string;
  /**
   * The host the wildcard sits under, fully resolved —
   * e.g. `backend-pr-123.preview.example.com`.
   */
  host: string;
  depth: SubdomainDepth;
  /** The env var carrying the rule, e.g. `PSTACK_WILD_BACKEND`. */
  varName: string;
  /** The Traefik rule, ready to paste into a router label. */
  rule: string;
};

/**
 * Escape a hostname for embedding in a Go regexp.
 *
 * Only `.` needs it in practice — a hostname cannot contain the other metacharacters — but escaping
 * the full set costs nothing and means a future relaxation of what a host may contain cannot turn
 * this into a rule that matches more than intended. An unescaped dot is the classic version of that
 * bug: `backend-pr-1.example.com` would also match `backend-pr-1Xexample.com`.
 */
export function escapeHostRegexp(host: string): string {
  return host.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * The Traefik rule for one wildcard host.
 *
 * `depth: one`  →  exactly one extra label: `api.<host>` yes, `a.b.<host>` no.
 * `depth: any`  →  one or more: both of the above.
 *
 * Anchored at both ends, and the label class excludes `.` so `one` really means one. The bare host
 * itself is deliberately NOT matched — that is your existing exact-host router's job, and matching
 * it here would put two routers on the same hostname for no reason.
 */
export function wildcardRule(host: string, depth: SubdomainDepth = 'one'): string {
  const h = escapeHostRegexp(host);
  const label = '[a-z0-9]([a-z0-9-]*[a-z0-9])?';
  const prefix = depth === 'any' ? `(${label}\\.)+` : `${label}\\.`;
  // Traefik v3 rules quote their argument with BACKTICKS, not quotes.
  return `HostRegexp(\`^${prefix}${h}$\`)`;
}

/**
 * `backend` → `PSTACK_WILD_BACKEND`.
 *
 * Dashes become underscores because an environment variable name cannot hold a dash — which makes
 * `admin-api` and `admin_api` collide, so the caller checks for that (below) rather than letting one
 * profile's rule silently overwrite another's.
 */
export function subdomainVarName(profile: string): string {
  return `PSTACK_WILD_${profile.toUpperCase().replace(/-/g, '_')}`;
}

/** Parsed form of one `compose.subdomains` entry, before the host is resolved. */
export type SubdomainConfig = {
  profile: string;
  depth: SubdomainDepth;
  /** Explicit host, if the spec gave one. Otherwise derived as `<profile>-<stack>.<domain>`. */
  host?: string;
};

/**
 * Parse `compose.subdomains`.
 *
 * Two accepted shapes, because the short one covers the common case and the long one is needed the
 * moment a hostname does not follow the convention:
 *
 *     subdomains: [backend, frontend]                  # depth: one, host derived
 *     subdomains:
 *       backend: any                                   # just the depth
 *       frontend: { depth: one, host: app-${STACK}.${DOMAIN} }
 */
export function parseSubdomains(
  raw: unknown,
  where: string,
  interp: (v: string, at: string) => string,
): SubdomainConfig[] {
  if (raw === undefined) return [];

  const depthOf = (v: unknown, at: string): SubdomainDepth => {
    if (v === undefined) return 'one';
    if (v !== 'one' && v !== 'any') {
      throw new SpecError(
        `${at} must be "one" (a single label — what DNS and TLS can cover) or "any" ` +
          `(any depth, routing only: no certificate can cover it). Got ${JSON.stringify(v)}.`,
      );
    }
    return v;
  };

  if (Array.isArray(raw)) {
    return raw.map((p, i) => {
      if (typeof p !== 'string') {
        throw new SpecError(`${where}[${i}] must be a profile name`);
      }
      return { profile: interp(p, `${where}[${i}]`), depth: 'one' as const };
    });
  }

  if (typeof raw !== 'object' || raw === null) {
    throw new SpecError(`${where} must be a list of profile names or a mapping of profile to options`);
  }

  return Object.entries(raw as Record<string, unknown>).map(([profile, v]) => {
    const at = `${where}.${profile}`;
    if (typeof v === 'string' || v === undefined || v === null) {
      return { profile, depth: depthOf(v ?? undefined, at) };
    }
    if (typeof v !== 'object' || Array.isArray(v)) {
      throw new SpecError(`${at} must be a depth ("one"/"any") or a mapping of { depth?, host? }`);
    }
    const m = v as Record<string, unknown>;
    for (const k of Object.keys(m)) {
      if (k !== 'depth' && k !== 'host') {
        throw new SpecError(`${at}.${k} is not a known option — expected \`depth\` or \`host\``);
      }
    }
    const host = m.host === undefined ? undefined : interp(String(m.host), `${at}.host`);
    return { profile, depth: depthOf(m.depth, `${at}.depth`), host };
  });
}

/**
 * Resolve every configured subdomain into a concrete route.
 *
 * `domain` is required and comes from the spec's own `env: DOMAIN:` — the same variable a per-PR
 * compose file already interpolates for its exact-host routers, so there is nothing new to declare
 * in the usual case. It is a HARD error when missing rather than a rule anchored on an empty
 * suffix: `HostRegexp(^[a-z0-9-]+\.$)` matches nothing at all, and a router that silently never
 * fires is worse than a spec that refuses to parse.
 */
export function resolveSubdomains(args: {
  configs: SubdomainConfig[];
  stack: string;
  profiles: string[];
  domain?: string;
}): SubdomainRoute[] {
  const { configs, stack, profiles, domain } = args;
  if (configs.length === 0) return [];

  if (!domain) {
    throw new SpecError(
      '`compose.subdomains` needs a domain to anchor its rules to. Declare it in the spec ' +
        '(`env:\n  DOMAIN: preview.example.com`) or give each entry an explicit `host:`. ' +
        'Without it the generated rule would match nothing.',
    );
  }

  const seenVar = new Map<string, string>();
  return configs.map((c) => {
    // A subdomain rule for a profile that is never brought up is dead config, and the likeliest
    // cause is a typo in one of the two lists — so say which names ARE available.
    if (!profiles.includes(c.profile)) {
      throw new SpecError(
        `compose.subdomains names profile "${c.profile}", which is not in compose.profiles ` +
          `(${profiles.length ? profiles.join(', ') : 'empty'}). Its router would never match a ` +
          `running service.`,
      );
    }
    const varName = subdomainVarName(c.profile);
    const clash = seenVar.get(varName);
    if (clash !== undefined) {
      throw new SpecError(
        `profiles "${clash}" and "${c.profile}" both map to ${varName} (a dash and an underscore ` +
          `are the same character in an environment variable name), so one rule would overwrite ` +
          `the other. Rename one profile.`,
      );
    }
    seenVar.set(varName, c.profile);

    const host = c.host ?? `${c.profile}-${stack}.${domain}`;
    return { profile: c.profile, host, depth: c.depth, varName, rule: wildcardRule(host, c.depth) };
  });
}

/**
 * The env additions handed to `docker compose`, so a label can interpolate `${PSTACK_WILD_<P>}`.
 *
 * Compose interpolates the compose FILE from its process environment and does not re-scan the
 * substituted value, so the `$` anchor and the backticks in the rule arrive at Traefik intact — and
 * no `$$` escaping is needed, which it would be if the rule were written into the file by hand.
 */
export function subdomainEnv(routes: SubdomainRoute[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const r of routes) out[r.varName] = r.rule;
  return out;
}
