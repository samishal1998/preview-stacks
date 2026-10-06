# Tasks stuck in `New` — the preview-ingress address leak

> Found on 2026-10-06 on a pstack 0.39.0 swarm host (1 manager, 7 workers, docker 29.7.2).
> Fixed for now by restarting docker on the manager. This document is what to do so it doesn't
> come back.
>
> **Fixes 1 and 2 shipped in 0.41.0:** new swarm hosts create both networks as `/16`s, an existing
> host can enlarge them with one command (below), and a task stuck in `New` now fails the deploy's
> readiness after a minute and shows in `GET /api/signals`, both pointing at docker's log. Fix 3 —
> finding which operation leaks — is still open.

## What happened

A preview deployed "successfully", but some of its containers sat in `New` forever — never placed
on a machine, never started. Everything that depended on them broke with them. In pr-19044 it was
`inngest`, and because `web` and `db-seed` needed it, those never ran either.

The containers that got stuck were exactly the ones attached to **`preview-ingress`**, the network
every preview's routed services share with traefik. Docker's own log (not `docker service ps`, which
showed no error at all) said why:

```
Failed allocation for service … error="could not find an available IP while allocating VIP"
```

`preview-ingress` is a `/24`: room for about 250 addresses. Swarm said all of them were taken. But
only about 5 things were actually using it — traefik, pstack, the UI, the network's own endpoint and
one service. No task, no node, nothing in swarm's records held the other ~245.

So swarm had handed addresses out over time and **never taken them back**. Because those addresses
belonged to no stack, removing stacks and pruning networks could not free them — and the network
itself is never pruned, because traefik is always attached to it.

Swarm keeps its "which addresses are taken" list in memory on the manager, and only rebuilds it from
its real records when docker restarts there. Docker had been running since the day the network was
created (Aug 31). The first failure in the log was **Sep 9** — every routed service deployed after
that day failed the same way.

## Right now: how to recognise it and clear it

**Recognise it** — on the manager:

```bash
journalctl -u docker --since "1 hour ago" --no-pager | grep "available IP"
```

Any output means this problem. A container stuck in `New` with this empty is something else.

**Clear it** — restart docker on the manager:

```bash
systemctl restart docker
```

Every preview URL and the control panel go dark for 10–30 seconds while traefik and pstack restart
(they are `restart: unless-stopped`, so they come back on their own). Containers on the workers keep
running. Stuck tasks get their address within seconds. Confirm with:

```bash
journalctl -u docker --since "2 min ago" --no-pager | grep -c "available IP"   # 0
```

This clears the backlog. It does **not** stop the leak, so on that host it will fill up again —
roughly nine days the first time, depending on how much preview traffic there is.

## Suggested fixes

In the order I would do them.

### 1. Make `preview-ingress` much bigger — the real fix

**What:** create `preview-ingress` with a `/16` (about 65,000 addresses) instead of docker's default
`/24`.

**Why this first:** it removes the cliff whether the cause is a leak or honest growth. Every routed
service in every preview takes an address there (one per service, one per running copy), plus one
per machine in the swarm — so a busy host fills a `/24` even with no leak at all. At the leak rate
seen here, a `/16` lasts years instead of nine days.

**Shipped in 0.41.0.** `--ingress-subnet` / `PSTACK_INGRESS_SUBNET` and `--shared-subnet` /
`PSTACK_SHARED_SUBNET`, defaults `10.250.0.0/16` and `10.251.0.0/16`. An existing network is kept
unless you ask, so `pstack upgrade` never takes previews down by surprise; `init` notes a network
smaller than the default instead. To enlarge one, with every preview on it asleep or down:

```bash
PSTACK_INGRESS_SUBNET=10.250.0.0/16 pstack upgrade
```

`init` checks for anything still attached — services on any machine, not just containers on the
manager — before it takes the control stack down, and stops naming them. What follows is the
reasoning as written before the change.

**Where:** `pstack init` creates the network in `internal/initctl/init.go`, step 2:

```go
runner.Run("docker network create "+createArgs+net+" 2>/dev/null || true", …)
```

`createArgs` is `-d overlay --attachable` under swarm and has no `--subnet`, which is why docker
picked a `/24`. Add one for `preview-ingress` under swarm, settable as a flag and an environment
variable like every other input (for example `--ingress-subnet`, default something like
`10.250.0.0/16`). `preview-shared` deserves the same treatment if many previews attach to it.

**Existing hosts:** a network's subnet can't be changed in place, so the network has to be removed and
created again — and docker only allows that when nothing is attached. `init` already has exactly this
path for a network with the wrong driver (take the control stack down, remove the network, create it
again, and refuse by name if a preview is still attached). The subnet check slots in next to it. For
an operator it means:

1. Put every preview to sleep or tear it down (anything on `preview-ingress` blocks the swap).
2. Run `pstack upgrade` (or `pstack init`) on a version with the change. It swaps the network.
3. Wake or redeploy the previews.

All previews are down for the duration, so this is a planned change, not a hotfix.

**Check before shipping:**

- Pick a range that cannot collide. Docker refuses to create a network that overlaps an existing one,
  and the range must not clash with the host's own LAN or the ranges docker gives other networks.
- Run it through the QEMU setup in [swarm-local-testing.md](swarm-local-testing.md) first: create the
  network, deploy a few previews, confirm their services get addresses from the new range.

**Effort:** small code change, one golden update, a documented migration.

### 2. Tell the operator when a task is stuck in `New`

**Shipped in 0.41.0.** Readiness fails a task still `new` a minute into the watch, with the reason
`never allocated: swarm gave it no address or machine` and the `journalctl` line to run; the
`container.start-failed` and `stack.failed` events carry it. `GET /api/signals` lists the task under
`stuck` with the same sentence — so a machine manager reading `reason` does not buy a machine for it.

**What:** pstack should notice a task that has sat in `New` for more than a minute or two and say so,
in words, with the hint to check docker's log.

**Why:** this failed silently for four weeks. The deploy job succeeded, because `docker stack deploy`
only submits the work. The UI showed the container as `New`. Readiness eventually gave up on the
stack without saying why. Even `GET /api/signals` (0.41.0) misses it: it only reports tasks swarm
explicitly refused to place (`no suitable node`), and these never got that far.

**Where:**

- **Readiness** (`internal/readiness`), which already watches every task of a freshly deployed stack:
  a task still `New` after a minute becomes a finding — *"swarm has not allocated this task. On the
  manager, `journalctl -u docker | grep 'available IP'` will say if its network is out of
  addresses."*
- **Signals** (`internal/signals`): add tasks stuck in `New` to `stuck`, with that same sentence as
  the reason, so a machine manager doesn't buy machines for a problem a machine won't fix.

pstack cannot read docker's log itself (it runs in a container), so it can only point at it — but
"stuck in `New` for ten minutes" alone is enough to know something is wrong.

**Effort:** small. The task state is already read; this is a timer and a message.

### 3. Find out what leaks the addresses

**What:** reproduce the leak on the local QEMU swarm and pin down which operation causes it.

**Why:** fixes 1 and 2 make the leak survivable and visible. Knowing the cause decides whether pstack
can stop doing the leaking thing, or whether it's a docker bug to report with a reproduction.

**Suspects, most likely first:**

1. **Sleep and wake.** Each one is `docker stack rm` followed later by `docker stack deploy` — every
   wake hands out fresh addresses, and every sleep should hand them back.
2. **The control stack being recreated.** traefik, pstack and the UI are plain containers attached
   to an attachable swarm network; every `pstack upgrade`, `pstack logging` or control restart
   recreates them and takes new addresses.
3. **Services removed before they were ever placed** — every stuck preview that got torn down while
   still `New`.

**How:** on a deliberately tiny network (a `/28`, 14 addresses), run each suspect in a loop and
watch the moment allocation starts failing. The suspect that exhausts it is the leak.

**Effort:** an afternoon with the QEMU setup.

### 4. Optional: make the clear-out free of downtime

Swarm also rebuilds its address list whenever a **different** manager becomes leader. With three
managers, you can hand leadership to another one (`docker node demote` the current leader from a
second manager, then promote it back) and nothing restarts — no traefik outage.

The cost: two more managers, which changes how the cluster behaves when machines fail (swarm needs
a majority of managers alive), and the drained workers would have to stay up permanently. Worth it
only if the leak turns out to be unfixable and frequent.

### 5. Stopgap only: restart docker on a schedule

A weekly `systemctl restart docker` on the manager, at a quiet hour, would have prevented this
incident. It also schedules a weekly outage of every preview, and it hides the problem rather than
fixing it. Use it only to buy time until fix 1 ships.

## What does not help

| Tried or tempting | Why it doesn't work |
|---|---|
| Removing stacks | The leaked addresses belong to no stack. |
| `docker network prune` | It never touches `preview-ingress` — traefik is always attached. |
| `docker service update --force` on the stuck service | It asks for a new address from the same full list. |
| Changing the swarm's default address-pool size | It applies to every network created afterwards, not this one, and changing it means re-creating the swarm. |

## In one line

Restart docker on the manager to unblock it today; ship a `/16` `preview-ingress` and a "stuck in
`New`" warning so it can't come back silently; then find which operation leaks.
