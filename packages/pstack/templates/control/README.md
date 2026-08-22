# The control stack

The always-on pair that turns a host into a pstack control plane: **Traefik** (ingress + one
wildcard certificate) and **pstack** (HTTP API + web UI in one container).

```
host (systemd / CLI)
  └─ pstack init ──▶ CONTROL stack: traefik + pstack
                                      │ manages
                                      ▼
                          shared + isolated deployments
```

## What `pstack init` writes, and where

Everything lands under `--data-dir` (default `/var/lib/pstack`, the same path the API reads as
`PSTACK_DATA`):

```
/var/lib/pstack/
├── deployments/                 the Registry — one directory per submitted deployment
└── control/
    ├── docker-compose.yml       ← a byte-for-byte copy of this directory's template
    ├── .env             0600    DOMAIN, ACME_EMAIL, DNS_PROVIDER, PSTACK_IMAGE, PSTACK_TOKEN
    ├── dns.env          0600    the DNS-01 credential, under the variable name lego expects
    └── traefik-dynamic/         the file provider's watched directory (starts empty)
```

`init` also creates the two external Docker networks — `preview-ingress` ("who may Traefik route
to") and `preview-shared` ("who may reach shared infrastructure") — then runs
`docker compose -p pstack-control … up -d` and waits for the pstack container's healthcheck to
pass before reporting success.

It is idempotent. Re-running it is the supported way to change the domain, rotate the token, or
move to a new image.

## The self-management rule

**The pstack API never manages the stack it runs in.**

`up` on the `pstack-control` project recreates the pstack container — killing the process
performing the operation, mid-operation. The HTTP request never returns, the job transcript dies
with the process (job history is in-memory by design), and if the new image is broken the host is
left with no control plane and no remote way to repair it: the only thing that could have fixed it
is what just died. A self-upgrade is not a feature with a caveat, it is a way to brick a host from
a browser tab.

So the host keeps that power. `pstack init` and `pstack upgrade` run over SSH, from systemd,
or from CI-with-a-key — always from outside the containers they are recreating, which is the only
place a failed upgrade is recoverable. That is also why this compose file lives *next to* the
registry instead of being submitted to it as a deployment: there is no id you could accidentally
`down`.

## Isn't ingress a non-goal?

`AGENTS.md` lists "ingress management, TLS issuance" among the PaaS features pstack does not do,
and that still holds for **deployments**: no spec field configures ingress, no route is created per
deployment, and routing a preview stays what it always was — a Compose label your own file
carries.

What Traefik does here is narrower. It terminates TLS for the **control plane's own UI**
(`pstack.<domain>`), and it requests **one** wildcard certificate that every other router on the
host inherits by SNI. That single always-on router is a prerequisite for previews being cheap: with
a wildcard record plus a wildcard cert, a new per-PR hostname needs no DNS step, no certificate
step, and no teardown step — which is exactly why `ingress` in `examples/preview.yml` has an
`assert_gone` but no `up`.

The trap to know: `certresolver` on a router makes Traefik request a certificate for *that
router's* domains. Exactly one router (the pstack one, in this template) may set it. Every per-PR
router sets `tls=true` and nothing else. N routers each requesting their own certificate is how you
meet Let's Encrypt's rate limits.

## Two Docker socket mounts, deliberately different

| Service | Mount | Why |
|---|---|---|
| `traefik` | `:ro` | It only enumerates containers, labels and networks. Read-only stops accidents — not an attacker, who can still read every container's environment through it. |
| `pstack` | read-write | It drives `docker compose`. This is root-equivalent on the host, and it is the entire reason `PSTACK_TOKEN` matters: anyone who can call the API can start a privileged container. |

Do not "fix" either one to match the other.

For the same reason the pstack service overrides the image's non-root default with
`user: "0:0"`: a non-root uid holding a read-write Docker socket is no boundary at all, while
costing two real failure modes (the socket's group id differs per distro; files written into the
bind-mounted registry would belong to a uid the host does not have).

## Related

- [`docs/bootstrap.md`](../../docs/bootstrap.md) — building the host itself (networks, DNS,
  sizing, hardening, and the ACME/404 troubleshooting tables).
- [`examples/shared.yml`](../../examples/shared.yml) — the *other* always-on stack: shared
  services previews borrow. That one is a deployment; this one is not.
