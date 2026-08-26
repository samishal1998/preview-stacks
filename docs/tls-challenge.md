# Switching the TLS challenge

A migration playbook, not a reference. **What each mode *is*** — the flags, the rate limits, the
arithmetic that decides between them — is [usage.md §7 Choose a TLS mode](usage.md#choose-a-tls-mode);
this page is the four commands that move a host that already exists, and the one step everybody
misses.

## Which mode am I on?

Read it from the running Traefik, never from what you remember configuring:

```bash
pstack api deployments runtime get --id pr-123 | jq -r .challenge
# → http01
```

In the UI it is the **TLS:** chip in the Routing panel of any deployment's Containers & routes tab.
`unknown` means the control stack could not be inspected, which is not the same as "neither".

## Why you would move to DNS-01

One sentence each; the numbers are in usage.md.

- **HTTP-01 issues one certificate per hostname**, on that hostname's first HTTPS request. So a host
  with many stacks spends its Let's Encrypt budget (~50 new certificates per registered domain per
  week) three previews at a time, and past that issuance fails as a browser TLS error on a preview
  that deployed perfectly.
- **A preview URL is invalid until its container exists**, because HTTP-01 needs something answering
  on the hostname being certified. DNS-01's wildcard is valid before the stack is.
- **A sleeping stack's waking page has no certificate** if its hostname was never live, because the
  catch-all router cannot ACME a `HostRegexp`. Under DNS-01 the wildcard covers it.

"TLS is slow on this host" is almost always the first of those.

## Moving to DNS-01 with Cloudflare

### 1. A token with two permissions

Cloudflare → **My Profile → API Tokens → Create Token → Custom**:

| Permission | Scope |
|---|---|
| `Zone` · `Zone` · **Read** | the zone your preview domain is in |
| `Zone` · `DNS` · **Edit** | the same zone |

One token, both permissions. A token missing `Zone:Read` fails as an ACME **propagation timeout**,
which sends you debugging DNS instead of the credential.

### 2. Take the host's CURRENT init line — do not write one from memory

Re-running `init` is the only supported way to change this, and **`init` re-renders from its
arguments alone. It reads nothing back.** Every flag you leave off reverts to that flag's *default*,
silently and host-wide:

| Left off | What happens |
|---|---|
| `PSTACK_TOKEN` | a **new machine token is minted** — every CI job holding the old one starts getting 401s |
| `PSTACK_DNS_TOKEN` (on a host that has one) | `dns.env` is rewritten blank |
| `--ui advanced` | the host reverts to the **basic** UI |
| `--orchestrator <what it is>` | defaults to `swarm` |
| `--dns-provider` | ignored under `http01`, **required** under `dns01` |

So do not compose the command by hand. The host can print its own:

```bash
pstack upgrade -n | grep 'pstack init'
# → pstack init --domain preview.example.com --acme-email ops@example.com \
#     --challenge http01 --ui advanced --orchestrator swarm
```

That is `initFlags`, the same builder an upgrade uses to re-init without changing anything nobody
asked to change. The token comes from `control/.env` on the host.

### 3. Run it again with the challenge changed, and nothing else

**Over SSH, on the box** — never from inside the control stack.

```bash
# every flag from the line above, with only the challenge and provider changed
PSTACK_TOKEN=$(. /var/lib/pstack/control/.env; echo "$PSTACK_TOKEN") \
PSTACK_DNS_TOKEN=<the-cloudflare-token> \
pstack init --domain preview.example.com --acme-email ops@example.com \
  --challenge dns01 --dns-provider cloudflare \
  --ui advanced --orchestrator swarm
```

It re-renders `control/docker-compose.yml`, writes `control/dns.env` (mode `0600`,
`CF_DNS_API_TOKEN=…`), brings the control stack up and waits for its healthcheck.

**Nothing else is touched** when the other flags match what the host already had. No machine to
recreate, no previews torn down, no networks recreated — that last one is the *orchestrator* switch,
not this. Your DNS records do not change either: you already need `*.preview.example.com` and
`preview.example.com` pointing at the box in both modes.

### 4. Redeploy every stack — this is the step people miss

Per-PR routers are labelled **at deploy time from the mode Traefik was running then**, and the rule
inverts between modes:

| Mode | Every per-PR router carries |
|---|---|
| `http01` | `tls=true` **and** `tls.certresolver=le` — no wildcard exists to inherit |
| `dns01` | `tls=true` **and nothing else** — one always-on router holds the wildcard, everyone else gets it by SNI |

So the stacks that were deployed under HTTP-01 still carry `certresolver=le`. Left alone, each one
orders **its own certificate** instead of inheriting the wildcard — which spends the weekly budget
you just switched modes to stop spending.

```bash
for id in $(pstack api deployments list | jq -r '.deployments[].id'); do
  pstack api deployments up --id "$id"
done
```

New deploys are correct from the moment `init` finishes; this is only for what was already there.

## Do not hand-edit the Traefik file

`control/docker-compose.yml` is **owned by `init`**, which re-renders it wholesale. An edit works
until the next `init` or `pstack upgrade` silently reverts it — and the symptom is a missing
certificate, which looks like an ACME problem rather than a lost edit. `dns.env` would survive; the
Traefik flags would not.

## Rolling back

Symmetric — re-run `init` with the other mode:

```bash
PSTACK_TOKEN=… pstack init --domain preview.example.com --acme-email ops@example.com \
  --challenge http01 --ui advanced --orchestrator swarm
```

Same rule as step 2: **every other flag has to be the one the host already had.** Then redeploy the
stacks again, for the same reason as step 4 in the other direction: they are
carrying `tls=true` alone and need `certresolver=le` back, or those hostnames have no certificate at
all.

## While you are waiting for a certificate

A CI job polling the preview's own hostname is waiting on ACME, not on the app. Ask the control
plane instead — it answers on a hostname whose certificate has been warm since `init` ran, and needs
no token:

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://api.preview.example.com/api/probe/pr-123
```

See [Probe a preview without a token](usage.md#probe-a-preview-without-a-token-0340).
