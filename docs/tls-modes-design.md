# API-driven TLS modes and `dns-persist-01` — a design, partly built

> **Phase 1 — the control-stack runtime view and restart — SHIPPED in 0.35.0**; see
> [usage.md → Watch the control stack itself](usage.md#watch-the-control-stack-itself-0350). The
> rest — `dns-persist-01`, the certd sidecar, API-driven mode switching — records the shape agreed
> on 2026-08-27 and is NOT built. Kept for the same reason [`mcp-design.md`](mcp-design.md) is:
> cheap to write down now, expensive to re-derive later.

## Why this exists

Two incidents shaped it. A host at real PR volume burned Let's Encrypt's 50-certificates-per-week
budget under HTTP-01 (the limit bills the *registered domain*, so "a different subdomain" is the
same bucket), and its Traefik — OOM-killed at 256m — lost its **in-memory** HTTP-01 challenge
tokens mid-validation, because Traefik CE has no external ACME storage; that is a Traefik
Enterprise feature, not a flag we forgot. Moving certificates out of Traefik's process is the only
way issuance survives a restart, and moving mode changes into the API is the only way a switch
stops being an SSH session with a foot-gun (`init` re-renders from its arguments alone).

## The finding that makes it cheap

**The file provider is already the API's writing hand.** `control/traefik-dynamic` is a watched
directory the API already writes (middlewares, the fallback router), mounted read-only into
Traefik. A wildcard certificate dropped there as a `tls.certificates` entry reaches Traefik with
**no restart, no init re-render, no control-compose change**. Everything below leans on that.

## `dns-persist-01` — what it is

The same DNS-01 challenge and the same vendor DNS token `--challenge dns01` takes today. What
changes is *who* runs it: a **lego sidecar**, not Traefik's resolver.

- **Not a control-stack member.** The sidecar is a pstack-managed container (its own compose
  project, working name `pstack-certd`), created and destroyed with powers the API already has.
  The control compose stays init-owned, byte for byte. This *sidesteps* the "control stack never
  manages itself" rule instead of weakening it.
- lego obtains `*.<domain>` + `<domain>`, writes the PEM pair to a volume shared with the API,
  and the API renders a `tls.certificates` file into `traefik-dynamic` — validated and renamed
  into place like every other dynamic file (an unparseable file can take the directory down).
- **Renewal**: the sidecar re-runs on a timer (lego's own renew window, ~30 days before expiry).
  Traefik's file provider re-reads on *dynamic-file* change, not cert-file change, so the renew
  hook rewrites (touches) the dynamic file — that nuance is the whole renewal bug surface.
- **Per-PR routers carry `tls=true` and nothing else**, exactly the dns01 rule: the wildcard is
  served from the store and inherited by SNI. `DetectChallenge` grows a third answer, checked
  first: certd running + cert file present → `dns-persist-01`; else Traefik argv as today.
- The control routers' baked-in labels (`certresolver=le` under http01) stay: two hostnames,
  renewal-exempt, harmless — and the wildcard covers them anyway once it exists.

**What survives a Traefik restart**: everything. The PEMs are files; there are no in-flight
challenge tokens in anyone's memory; a restart re-reads the directory.

**What was deliberately NOT chosen** (asked and answered): acme-dns-style CNAME delegation — the
variant where the user pastes one `_acme-challenge` record and a DNS sidecar on port 53 answers
challenges without any vendor token. Real, and the natural second tier on the same certd plumbing,
but it doubles the infra surface (an authoritative DNS server, port 53 exposure) for a benefit
(tokenless) nobody needs yet. Revisit only if a host's DNS provider has no lego support.

## Switching modes through the API

A settings-shaped route (root/maintainer) holds the *desired* mode; a job reconciles:

| From → To | What the job does | init needed? |
|---|---|---|
| any → `dns-persist-01` | start certd, wait for the PEM, write the dynamic cert file, redeploy every stack | **no** |
| `dns-persist-01` → previous | remove the dynamic file, stop certd, redeploy every stack | **no** |
| `http01` ↔ `dns01` | Traefik's own flags change — the API refuses and prints the host's exact re-init line (`pstack upgrade -n`) | yes |

The **redeploy-every-stack loop moves server-side**: today it is a for-loop in
[`tls-challenge.md`](tls-challenge.md) run by hand; here it is one job that walks the registry and
queues an `up` per deployment through the existing jobs queue, reporting progress like any job.
The wired challenge probe (0.35.0) is what makes the regenerated labels honest.

**The token's trust posture changes, deliberately.** Today `dns.env` is Traefik's business and the
API *cannot read it* (the mount comment says so, on purpose). Under `dns-persist-01` the API
receives the token and hands it to certd's environment — the same posture as private-registry
credentials, so it is stored and redacted the way `registries` credentials already are, never in a
deployment's files. Anyone with the docker socket could read it from the container anyway; the
socket is already root-equivalent, so this changes ergonomics, not the threat model.

**Rate-limit honesty**: the wildcard order is itself one new certificate. A host that just burned
the weekly bucket sees the certd job fail with the CA's own 429 in the job output — surfaced, not
retried into the failed-validation limit.

## The control stack becomes visible

`GET /api/control/runtime` — the same shape as a deployment's runtime page, for the containers of
`pstack-control` (+ certd): state, health, image, started-at, **restart count and OOM-killed
flag**, mem limit. The OOM flag is the point: the incident above was diagnosed from the *default
certificate's notBefore timestamp* because nothing surfaced Traefik's restarts.

Mutations, with a hard line:

- **restart** for `traefik`, `advanced-ui`, `certd` — root/maintainer, audited like any job.
- **never** the `pstack` container, by name, with the refusal explaining why: recreating the
  container that is performing the operation kills the operation, and if the new image is broken
  the thing that could have repaired the host died with it. The template header's rule protected
  exactly this container all along; the API enforces it now instead of relying on distance.
- add/edit/remove of control-stack members stays init's job. certd is not an exception — it lives
  *outside* the control stack precisely so the API may own it.

## Cost, honestly

| | |
|---|---|
| certd lifecycle (compose project, image pin, config, PEM wait) | the real work |
| dynamic cert file render + renewal touch | small, but the bug surface |
| mode settings route + reconcile job + redeploy loop | moderate; jobs queue exists |
| `DetectChallenge` third answer + label rule | small (the probe is wired now) |
| `/api/control/runtime` + restart + refusal | moderate; inspect has the pieces |
| UI (settings section + control runtime tab) | moderate |
| conformance (fake lego, fake CA, mode-switch scenarios) | not small — it is the spec |

Phasing that pays out earliest: **(1)** the control runtime view — standalone, would have cut this
week's diagnosis from hours to seconds; **(2)** certd + `dns-persist-01` + the API switch;
**(3)** nothing — the init-gated transitions stay documented, not built.

## What stays out either way

acme-dns delegation (above), per-service management of a *deployment's* containers (a different
feature, scoped separately when asked), and any external ACME store for Traefik itself — CE cannot
do it, and with certs on disk there is nothing left for one to hold.
