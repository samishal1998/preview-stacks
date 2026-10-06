---
title: Concepts
description: Stacks, axes and their hooks, shared vs isolated, the control plane, and why teardown is the part that matters.
sidebar:
  order: 2
---

pstack gives an ephemeral preview stack a **declarative lifecycle**: the resources it needs are
provisioned around a Docker Compose project, torn down in reverse, and then **proven gone**.
Everything else — the API, the UI, notifications, sleep, swarm — is built around that.

## The spec

A preview is described by one file, normally `preview.yml`:

```yaml
version: 1
stack: pr-${PR}                     # the identity
kind: isolated                      # the default; `shared` is for host singletons
compose:
  file: docker-compose.preview.yml
  profiles: [backend, frontend]
axes:                               # the resources Compose knows nothing about
  - name: database
    up: ./hooks/db-branch.sh recreate "$STACK"
    assert_live: ./hooks/db-branch.sh count "$STACK" | grep -qx 1
    down: ./hooks/db-branch.sh delete "$STACK"
    assert_gone: |
      ./hooks/db-branch.sh ping || exit 1
      [ "$(./hooks/db-branch.sh count "$STACK")" = "0" ]
```

**The stack** is the preview's identity. It becomes the Compose project name and is exported to
every hook as `$STACK`. An unset variable in it is an error, never an empty string: `pr-` with `PR`
unset would be one stack shared by every pull request.

**Profiles.** `up` and `down` both enable every profile the spec lists, so a teardown never passes
fewer profiles than the deploy did. Put every service behind a profile, so a bare
`docker compose up` starts nothing.

## Axes and their four hooks

An axis is one stateful resource a preview needs that Compose cannot manage: a database branch, a
queue namespace, per-PR images, a DNS record. Each has up to four shell hooks, and the asymmetry
between them is the whole product:

| Hook | Contract | On failure |
|---|---|---|
| `up` | create it — and be safe to run again, because a redeploy reruns it | stops the run |
| `assert_live` | exit 0 means it exists | stops the run — catches an `up` that lied |
| `down` | destroy it | recorded, never fatal |
| `assert_gone` | exit 0 means it is gone | the run exits `2`: **leaked** |

`up` fails fast because a half-provisioned preview should not be deployed. `down` never stops,
because stopping halfway leaves more behind than carrying on. `assert_gone` is strict, because a
teardown that half-worked and said nothing is exactly what this tool exists to catch.

Axes come up top to bottom and go down bottom to top, so declare a dependency before whatever
needs it. A hook can hand values onward: any `SHOUT_CASE=value` line an `up` hook prints is passed
to Compose and to the hooks after it, which is how a fresh database hands over its URL with no temp
file.

Hooks are shell strings run with `bash -c`. A spec is as trusted as a CI workflow file — pstack does
not sandbox it.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | it worked |
| `1` | it failed |
| `2` | torn down, but something **leaked** |
| `3` | bad spec or bad usage |

`2` exists so CI can treat "teardown left something behind" differently from "teardown errored" —
they are different problems with different owners.

## Isolated and shared

| | `kind: isolated` (default) | `kind: shared` |
|---|---|---|
| What | one tenant, normally one pull request | a singleton every preview borrows: a database, a queue cluster |
| Axes | yes — the point | none allowed |
| `down` | routine | refused without `--force` |

An isolated preview lists what it depends on under `requires:` — each entry a `name`, an `assert`
shell command that proves the dependency is there, and an optional `hint`. `up` runs every assert
before it creates anything, so a missing dependency fails by name instead of halfway through a
hook.

## The control plane

On a host, `pstack init` stands up a small control stack: Traefik for routing and TLS on your
domain, the pstack API, and a web UI at `control.<domain>`. From then on previews are
**deployments**: submit a spec over the API — usually from CI — and pstack runs `up`, `down` or
`verify` as a **job**, one at a time per preview, with a log you can follow.

- A service that asks to be routed gets a hostname, `<name>-<stack>.<domain>` — `web-pr-123.<domain>`
  — unless it names its own.
- Notifiers deliver signed webhooks (and Slack or Discord messages) for every deploy, failure and
  leak.
- Accounts, single sign-on and four roles divide what people may do; share links let someone
  without an account see one preview's details and logs.

pstack is deliberately not a reconciler. It keeps a record of what it was asked to deploy, but what
actually exists is always read from Docker and from each axis's own `assert_*` probe — never from a
row pstack wrote.

## Compose or Swarm

A host runs previews one of two ways:

- **Compose** — every preview on this one machine.
- **Swarm** — the host is a swarm manager, and previews spread across worker machines that join
  it. pstack converts each Compose file to what `docker stack deploy` accepts and names every change
  it makes.

On either, a preview can **sleep** after a period without traffic: its containers are taken down,
its volumes and axes stay, and the next request to its hostname brings it back.

## Read on

- [Quick start](/preview-stacks/start/quick-start/) — all of the first half of this page, run.
- [Design](/preview-stacks/start/design/) — why pstack is shaped this way.
- [The guide](/preview-stacks/guide/overview/) — every task, with the commands and their output.
