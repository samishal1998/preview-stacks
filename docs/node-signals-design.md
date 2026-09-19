# Node signals — what pstack tells a machine manager

> Status: rewritten and simplified 2026-09-20, not built. Build plan:
> [node-signals-plan.md](node-signals-plan.md).

## The job

pstack runs preview stacks across a swarm: one manager (the primary) and any number of workers. It
does not create or delete machines, and it is not going to start. Something else does that — a
script, a person, or Fleet Plane.

That something needs two answers from pstack, and pstack is the only thing that can give them:

1. **"Do you need another machine?"** — yes when docker says a task cannot be placed.
2. **"Can I take this machine away?"** — yes when a worker is running nothing.

That is the whole feature. pstack reports; the other side decides and acts.

**Swarm only.** A single-machine host has nothing to report.

## What pstack reports

### `GET /api/signals`

Asks docker, answers straight away. Nothing is stored.

```json
{
  "v": 1,
  "swarm": true,
  "reachable": true,
  "at": 1758358800000,
  "nodes": [
    { "id": "xk3f…", "hostname": "worker-3", "role": "worker", "state": "ready",
      "availability": "active", "tasks": 0, "emptySince": 1758357000000 }
  ],
  "stuck": [
    { "task": "j8d2…", "service": "pr-42_web", "stack": "pr-42",
      "reason": "no suitable node (insufficient resources on 2 nodes)" }
  ]
}
```

- **`nodes`** — every machine in the swarm, and how many tasks each is running. `tasks: 0` on a
  worker is the "you can take this away" answer. `emptySince` is when pstack first saw it empty (it
  forgets on restart, so the clock can only start late, never early).
- **`stuck`** — every task docker refused to place, with docker's own words for why. That is the
  "I need another machine" answer. The caller reads `reason` and decides whether a machine would
  actually help: `insufficient resources` means yes, `scheduling constraints not satisfied` usually
  means a spec needs fixing.

Anyone who can read the API can read this. It exposes no credentials.

### Two events

For people who would rather be told than ask. Both carry the same fields as above.

| Event | Fires when |
|---|---|
| `signal.raised` | a task gets stuck, or a worker goes empty |
| `signal.cleared` | it stops being true |

Only on the change, not repeatedly. If pstack restarts it re-sends whatever is still true, so a
listener that missed something catches up. Each payload carries an id like `empty/xk3f…` or
`stuck/pr-42_web` — act on that id, and make acting twice on the same id harmless on your side.

## What pstack does when asked

Three routes, one docker command each, for maintainers and above. None of them creates or destroys a
machine.

| Route | Runs | Notes |
|---|---|---|
| `POST /api/swarm/nodes/:id/drain` | `docker node update --availability drain` | Swarm stops putting work there and moves what is there. Do this before you delete a machine. |
| `POST /api/swarm/nodes/:id/undrain` | `docker node update --availability active` | Changed your mind, or the delete failed. |
| `DELETE /api/swarm/nodes/:id` | `docker node rm` | Only once the machine is gone (docker reports it `down`). Never forced. |

The normal sequence is: see an empty worker → drain it → read `GET /api/signals` again to confirm it
is still empty → delete the machine → `DELETE` the node from the swarm.

**Why drain matters.** Swarm prefers the emptiest machine, so the empty worker is exactly where the
next deploy lands. Draining takes it out of the running before you act on it.

## What this deliberately does not do

- **No capacity arithmetic.** pstack does not add up CPU and memory to work out whether a stack
  would fit somewhere. Two reasons: swarm only counts the reservations people write in their compose
  files, and hardly anyone writes them; and the machine manager already has a policy for how much
  spare capacity to keep. Reporting facts and leaving the policy there is simpler and more honest.
- **No settings, no new `init` flags.** Nothing to configure, nothing to keep in step across an
  upgrade.
- **No guessing about sleeping stacks.** A sleeping stack runs nothing, so it appears nowhere here.
  If it wakes and does not fit, its task shows up in `stuck` and the machine manager reacts — the
  same loop, one step later.
- **No provisioning.** No cloud credentials, ever.

## Caveats worth knowing before you use it

- **A worker's disks go with the machine.** If a stack kept data on a worker and you delete that
  worker, the data is gone. Under swarm this is already shaky — a stack that sleeps and wakes can
  come back on a different machine with an empty volume — so treat preview data as disposable.
- **"Empty" means no swarm tasks.** A manager cannot see plain containers on a worker, so a box
  somebody is using by hand looks idle.
- **The primary is never reported as empty.** It runs pstack itself, and the isolation-axis hooks
  run there too.
- **`stuck` is docker's opinion, passed through.** pstack neither re-words it nor diagnoses it.

## How it fits Fleet Plane later

Nothing here names Fleet Plane, and nothing needs to change when it arrives:

- a stuck task with `insufficient resources` → ask Fleet Plane for a machine
- an empty worker → drain it here, release the machine there, then `DELETE` the node

The machine is matched by hostname. Fleet Plane's own policies — how much headroom to keep, how long
to wait before reclaiming — stay on its side, which is where they belong.
