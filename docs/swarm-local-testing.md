# Testing swarm features on a local cluster

CI has one machine, so everything that needs a **real swarm with real workers** — node signals,
placement failures, draining a node, a node going down — is untested until it runs on a cluster.
This is how to get one on a laptop, with QEMU, in about fifteen minutes.

It is a throwaway. Nothing here belongs on a host anyone depends on.

## The shape

One Linux VM, and the swarm **inside** it as three docker-in-docker containers:

```
macOS ── QEMU VM (Debian arm64, docker) ── mgr   (dind, swarm manager, runs pstack)
                                        ├─ wrk1  (dind, worker)
                                        └─ wrk2  (dind, worker)
```

**Why not three VMs.** Three VMs need a shared network, which on macOS means `socket_vmnet` and
sudo, or QEMU's socket hub and a lot of flags. Three dind containers share a docker bridge, so
`docker swarm join` works with no networking setup at all. What matters for these tests is a real
swarmkit scheduler with real nodes, and dind gives exactly that: each container runs its own docker
daemon, its own volumes, and joins the swarm as its own node.

**Why a VM at all.** There is no docker on this Mac, and dind needs a Linux kernel.

What this cannot test: Traefik, TLS, DNS, and anything about cloud provisioning. None of that is in
the signals surface.

## What you need

| | |
|---|---|
| QEMU | `/opt/homebrew/bin/qemu-system-aarch64` — already installed |
| UEFI firmware | `/opt/homebrew/share/qemu/edk2-aarch64-code.fd` — ships with QEMU |
| Debian arm64 cloud image | `debian-13-genericcloud-arm64.qcow2`, ~400 MB |
| Disk | ~12 GB free under `~/.cache/pstack-vm` |
| RAM | 4 GB for the VM (the Mac has 16) |

Acceleration is HVF, so the VM runs at native speed.

## 1. Build the binary the VM will run

```bash
cd /Volumes/S1/code/preview-stacks
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/pstack-linux-arm64 ./packages/pstack/cmd/pstack
```

Static, so it runs in the dind container with nothing installed beside it.

## 2. Bring up the VM

```bash
mkdir -p ~/.cache/pstack-vm && cd ~/.cache/pstack-vm
curl -fLO https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-arm64.qcow2
qemu-img create -F qcow2 -b debian-13-genericcloud-arm64.qcow2 -f qcow2 disk.qcow2 20G
ssh-keygen -t ed25519 -N '' -f vmkey                      # a throwaway key
```

cloud-init, via a NoCloud seed made with the `hdiutil` that ships with macOS:

```bash
mkdir -p seed && cd seed
printf 'instance-id: pstack-swarm\nlocal-hostname: pstack-swarm\n' > meta-data
cat > user-data <<EOF
#cloud-config
users:
  - name: dev
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys: [$(cat ../vmkey.pub)]
package_update: true
packages: [docker.io, jq, curl]
runcmd:
  - [systemctl, enable, --now, docker]
  - [usermod, -aG, docker, dev]
EOF
cd .. && hdiutil makehybrid -o seed.iso -joliet -iso -default-volume-name cidata seed/
```

```bash
qemu-system-aarch64 -machine virt,highmem=on -accel hvf -cpu host -smp 4 -m 4096 \
  -drive if=pflash,format=raw,readonly=on,file=/opt/homebrew/share/qemu/edk2-aarch64-code.fd \
  -drive if=virtio,format=qcow2,file=disk.qcow2 \
  -drive if=virtio,format=raw,file=seed.iso \
  -netdev user,id=n0,hostfwd=tcp::2222-:22,hostfwd=tcp::7799-:7799 \
  -device virtio-net-pci,netdev=n0 -nographic
```

`hostfwd` gives you ssh on 2222 and pstack's API on 7799. Then, in another terminal:

```bash
ssh -i ~/.cache/pstack-vm/vmkey -p 2222 -o StrictHostKeyChecking=no dev@localhost
scp -i ~/.cache/pstack-vm/vmkey -P 2222 /tmp/pstack-linux-arm64 dev@localhost:pstack
```

## 3. Build the swarm

Everything from here runs **inside the VM**.

```bash
docker network create swarmnet
for n in mgr wrk1 wrk2; do
  docker run -d --privileged --name $n --hostname $n --network swarmnet \
    -e DOCKER_TLS_CERTDIR= docker:28-dind --host=tcp://0.0.0.0:2375 --host=unix:///var/run/docker.sock
done
sleep 10
d() { c=$1; shift; docker exec $c docker "$@"; }         # run a docker command on a node

d mgr swarm init --advertise-addr mgr
JOIN=$(d mgr swarm join-token -q worker)
d wrk1 swarm join --token $JOIN mgr:2377
d wrk2 swarm join --token $JOIN mgr:2377
d mgr node ls                                            # three nodes, mgr is the leader
```

Give the two workers a small memory reservation ceiling by deploying something that fits, and
something that cannot:

```bash
docker cp ~/pstack mgr:/usr/local/bin/pstack
d mgr service create --name fits --replicas 2 --reserve-memory 64M alpine sleep 1d
d mgr service create --name logs --mode global alpine sleep 1d          # a global service
d mgr service create --name toobig --reserve-memory 900G alpine sleep 1d  # will never be placed
d mgr service ps toobig --no-trunc                                       # "no suitable node (insufficient resources on 3 nodes)"
```

`toobig` is the whole point: it produces a genuine `no suitable node` error from swarmkit, which is
the string the signals parser depends on and which no unit test can prove is still spelled that way.

## 4. Run pstack and read the signals

```bash
docker exec -d -e PSTACK_TOKEN=devtoken -e PSTACK_PORT=7799 -e PSTACK_HOST=0.0.0.0 \
  -e PSTACK_ORCHESTRATOR=swarm -e PSTACK_SIGNALS_TICK_MS=5000 mgr pstack serve
```

From the Mac (port 7799 is forwarded):

```bash
H='Authorization: Bearer devtoken'
curl -s -H "$H" localhost:7799/api/signals | jq
```

## 5. What to check, and what each case proves

| # | Do this | Expect | Why it cannot be tested anywhere else |
|---|---|---|---|
| 1 | Read `/api/signals` | three nodes; `mgr` role `manager`; `stuck` names `toobig` with docker's sentence | The real error string from a real scheduler. The unit tests assert a string a human typed. |
| 2 | Check the task counts | `fits` counted, `logs` (global) not | Swarm decides where the two `fits` replicas land; the counts must add up against a real placement. |
| 3 | `d mgr service rm toobig` | `stuck` empties; a `signal.cleared` reaches a notifier | The clear path against real docker output. |
| 4 | `d mgr service update --replicas 0 fits` then wait | both workers report `tasks: 0` with an `emptySince` | "Empty" against a real cluster, including tasks in `Shutdown` that must not count. |
| 5 | `curl -X POST -H "$H" localhost:7799/api/swarm/nodes/<wrk2-id>/drain` | 200; `d mgr node ls` shows `Drain`; tasks move off it | The one write pstack makes to the cluster. |
| 6 | `DELETE` that node while it is still up | 409, and `d mgr node ls` unchanged | The refusal that protects a node which is merely unreachable. |
| 7 | `docker stop wrk2`, wait ~30s, then `DELETE` | `node ls` shows `Down`; the DELETE now returns 200 and the node is gone | The only way to see a node genuinely go `down`. |
| 8 | `docker start wrk2`; `d wrk2 swarm leave --force`; re-join | it comes back as a new node | Proves the loop can repeat — provision, use, reclaim. |
| 9 | Register a webhook notifier and watch across 3–8 | one `signal.raised` per change, no repeats | Delivery against a real ticker rather than a hand-driven one. |
| 10 | Deploy a real preview through the API with an axis | it schedules across the workers, and the signals stay consistent | The feature in context, not in isolation. |

For 9, the easiest receiver is a one-liner in the VM:

```bash
docker exec -d mgr sh -c 'while true; do nc -l -p 9000 | tee -a /tmp/hooks.log; done'
curl -s -X POST -H "$H" -H 'content-type: application/json' localhost:7799/api/notifiers \
  -d '{"type":"webhook","url":"http://mgr:9000","events":["*"]}'
docker exec mgr cat /tmp/hooks.log
```

## 6. Tear it down

```bash
docker rm -f mgr wrk1 wrk2 && docker network rm swarmnet     # inside the VM
# on the Mac: stop QEMU (Ctrl-A X), then
rm -rf ~/.cache/pstack-vm
```

## If something fails

Record it as a finding against `docs/node-signals-design.md`, not as a patch to the tests: the
value of this exercise is the gap between what the unit tests assert and what docker actually does.
The likely candidates, in order:

1. **The error sentence has changed** in a newer docker, so `stuck` comes back empty. The parser
   keys on the prefix `no suitable node`.
2. **A task in `Shutdown` is counted** as occupying a node, so nothing ever looks empty. The filter
   is `desired-state=running`.
3. **A node's hostname does not match** what `docker service ps` prints for `Node`, so task counts
   land on the wrong row. dind sets the hostname explicitly for exactly this reason.
