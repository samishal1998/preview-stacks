# Subdomain TLS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `compose.subdomains` subtree (and its custom-route mirror) serves HTTPS on a DNS-01 host, wakes a sleeping stack, and cannot swallow the host's own names.

**Architecture:** On dns01, autolabel's `-wild` router and the custom-route `-wild` router order one certificate for `<host>` + `*.<host>`. On other modes they carry `tls` without a resolver and report a note in the deploy log and `validate`. Both wake catch-alls widen to any depth. A subdomains host that names a served domain or a control host is refused.

**Tech Stack:** Go 1.23 (`packages/pstack`), Traefik v3.6.1 labels and file-provider YAML, bun conformance goldens.

**Spec:** [docs/subdomain-tls-design.md](subdomain-tls-design.md). Decisions D1–D9 there are binding.

## Global Constraints

- `export GOROOT=/usr/lib/go-1.23` before Go commands.
- Every new Go test carries a `// negative control:` line, and the control is run once to watch it fail (AGENTS.md).
- Goldens move only through `cd packages/conformance && bun gen/goldens.ts`, never `bun run gen`. The binary comes first: `cd packages/pstack && go build -o bin/pstack ./cmd/pstack`. Never regenerate `golden/host/**`.
- Traefik v3 rule syntax: `HostRegexp(` + backtick + Go regexp + backtick + `)`. In compose labels `$` is written `$$`.
- Copy is terse; state, do not explain (docs/ui-rules.md). Notes start with the service and say the fact: `web: *.web-pr-1.preview.example.com has no certificate on an http01 host (wildcards need DNS-01)`.
- CHANGELOG entries go under `## Unreleased` in `packages/pstack/CHANGELOG.md`.
- VCS writes go through `but` only. Branch `claude/subdomain-tls`, stacked on `claude/refinement-sweep`, because both touch `init.go` and the render goldens.

## Review Focus

1. An http01 host redeploying an existing subdomains spec: the deploy still succeeds, `-wild` loses its `certresolver`, and one note appears. Task 2 tests it.
2. A dns-persist-01 host: no `-wild` label or routes-file entry orders anything. Tasks 2 and 3 test it.
3. A custom route moved to another stack keeps byte-identical `tls.domains`, so `acme.json` is reused. Task 3 tests it.
4. `host:` equal to the preview domain, an added domain, or `control.<d>` is refused before any file is written. Tasks 1 and 2 test it.
5. The widened wake rule still loses to `control.`/`api.` exact routers, and a sleeping stack's `a.web-pr-1.<domain>` wakes it. Task 4 tests it.

---

### Task 1: Spec refuses a subdomains host that is the preview domain or its parent; validate tells the truth

**Files:**
- Modify: `packages/pstack/internal/spec/subdomains.go` (`ResolveSubdomains`; the package comment's "pstack does not own your per-PR Traefik labels" and "a DNS wildcard … ONE label" lines)
- Modify: `packages/pstack/internal/cli/run.go:~439-446` (the subdomains lines in validate)
- Test: `packages/pstack/internal/spec/subdomains_test.go`, `packages/pstack/internal/cli/validate_test.go`

**Interfaces:**
- Produces: `ResolveSubdomains` returns `*spec.Error` with message `` compose.subdomains.<profile>.host <host> contains the preview domain; its wildcard would catch every hostname on this host `` when `strings.EqualFold(host, domain) || strings.HasSuffix(strings.ToLower(domain), "."+strings.ToLower(host))`.

- [ ] **Step 1: Write the failing tests.** `TestSubdomainHostCannotBeThePreviewDomain`: ResolveSubdomains with `Host: &domain` and with `Host: "example.com"` under domain `preview.example.com` both error with the message above; `Host: "x."+domain` succeeds. `TestValidateSubdomainLines`: a spec with `subdomains: [web]` prints `  subdomains: *.web-pr-1.preview.example.com → web  (one label)` followed by `              router generated; ${PSTACK_WILD_WEB} is for a service with its own traefik labels`, then `              TLS: DNS-01 hosts order one certificate per deployment; other modes serve none`.
- [ ] **Step 2: Run them and watch them fail.** `go test ./internal/spec/ ./internal/cli/ -run 'SubdomainHost|ValidateSubdomain'`
- [ ] **Step 3: Implement.** The equality-or-parent check in ResolveSubdomains, plus the two reworded validate lines. For `depth: any`, keep the existing `(any depth — HTTP only, no cert can cover it)` line. Fix the two package-comment sentences named under Files: autolabel writes the router, and DNS `*.<domain>` covers deeper names while certificates do not.
- [ ] **Step 4: Run the tests and the controls.** `go test ./internal/spec/ ./internal/cli/`. Check that no validate golden moves: `rg -l subdomains packages/conformance/golden/cli` returns nothing.
- [ ] **Step 5: Commit.** `but commit -b <id> -m "fix(spec): a subdomains host cannot be the preview domain; validate says what is generated" <ids>`

### Task 2: Autolabel orders the subtree's certificate on dns01 and notes it elsewhere

**Files:**
- Modify: `packages/pstack/internal/autolabel/autolabel.go` (`AugmentResult`, `MaterializeResult`, the `-wild` block ~:398-417)
- Modify: `packages/pstack/internal/compose/compose.go:~150-157` (print the notes)
- Modify: `packages/pstack/internal/cli/run.go:~405-432` (validate prints them)
- Test: `packages/pstack/internal/autolabel/autolabel_test.go` (beside `TestGeneratedTraefikLabels`)

**Interfaces:**
- Produces: on both `AugmentResult` and `MaterializeResult`, `TLSNotes []string` (why a `-wild` router has no certificate) and `MatchNotes []string` (subdomains entries that attached to nothing), empty and never nil. `compose.ComposeUp` passes both to `note(n)`; validate prints only MatchNotes, because it runs with `Challenge: Unknown` and Task 1's line already states the TLS rule.
- Consumes: `ControlHostname(host string) bool` (existing, `autolabel.go:170`).

- [ ] **Step 1: Write the failing tests** (table over `Challenge`).
  - `DNS01`: the labels include `<id>-wild.tls.certresolver=le`, `<id>-wild.tls.domains[0].main=web-pr-7.preview.example.com` and `<id>-wild.tls.domains[0].sans=*.web-pr-7.preview.example.com`. TLSNotes is empty. The plain router still has no certresolver.
  - `HTTP01` and `Unknown`: no `-wild.tls.certresolver` label, and TLSNotes == [`web: *.web-pr-7.preview.example.com has no certificate on an http01 host (wildcards need DNS-01)`] (for unknown: `… on a host whose certificate mode is unknown`).
  - `DNSPersist`: no certresolver; the note reads `web: *.web-pr-7.preview.example.com is served only if the stored wildcard covers it`.
  - `depth: any` on DNS01: the pin is present, and the note reads `web: names deeper than one label under web-pr-7.preview.example.com get no certificate`.
  - A subdomains entry naming a profile that is no routed service's name: MatchNotes == [`subdomains entry "x" matches no routed service; no wildcard router`].
  - `TestWildcardHostCannotBeServed`: with `ControlHostname` stubbed to true for `control.preview.example.com`, a `host: preview.example.com` entry errors with `` service "web": compose.subdomains host preview.example.com is a domain this host serves `` and no file is written. Error text for a control host: `… is a control hostname`.
- [ ] **Step 2: Run them and watch them fail.** `go test ./internal/autolabel/ -run 'Wild|Subdomain'`
- [ ] **Step 3: Implement.**
  - In the `-wild` block, switch on `challenge`: DNS01 appends the three labels; any other value appends one note.
  - Before appending, refuse with `ControlHostname("control."+sub.Host) || ControlHostname(sub.Host)`.
  - After the service loop, add a MatchNote for each entry that never matched.
  - Thread both fields through MaterializeCompose and print them in ComposeUp after LogNotes. Validate prints MatchNotes as `  note: <n>`.
- [ ] **Step 4: Run the tests and the controls.** `go test -race ./internal/autolabel/ ./internal/compose/ ./internal/cli/`
- [ ] **Step 5: Commit.** `fix(autolabel): a subdomain subtree orders its certificate on dns01; other modes say why not`

### Task 3: Custom-route wildcards follow the same rules

**Files:**
- Modify: `packages/pstack/internal/routing/customroutes.go` (`RenderCustomRoutes`, the package comment's "TLS is never the dns01 wildcard pin" paragraph)
- Test: `packages/pstack/internal/routing/customroutes_test.go` (`TestCustomRoutesTLSByMode`, `TestCustomRoutesGolden`)

**Interfaces:**
- Consumes: `tlsFor(host, mode string, wildcardPin bool) string` (`domains.go`).

- [ ] **Step 1: Write the failing tests.**
  - In `TestCustomRoutesTLSByMode`, add a `-wild` row per mode:
    - `dns01`: the pin block, with `main: web-checkout.preview.example.com` and `sans: ["*.web-checkout.preview.example.com"]`.
    - `http01`, `unknown` and `dns-persist-01`: `tls: {}`.
    - In every mode, the plain router's block is unchanged.
  - `TestCustomRoutesWildPinFollowsTheAliasNotTheStack`: the same alias rendered for stack `a-pr-1` and stack `a-pr-2` gives identical `tls:` blocks.
- [ ] **Step 2: Run them and watch them fail.** `go test ./internal/routing/ -run CustomRoutes`
- [ ] **Step 3: Implement.** The inner `router` closure takes the TLS string instead of computing it. The plain router passes `tlsFor(host, mode, false)`. For `-wild`, dns01 passes `tlsFor(host, mode, true)` and every other mode passes `"      tls: {}\n"`. Rewrite the comment paragraph:
  > Plain alias routers inherit the host's certificate. A `-wild` router on dns01 pins its own `<host>` + `*.<host>`, one certificate per alias and subdomain service, because nothing else covers a second-level name.

  Update `TestCustomRoutesGolden` only if its expected text changes (it renders http01, so the `-wild` block becomes `tls: {}`).
- [ ] **Step 4: Run the tests and the controls.** `go test ./internal/routing/`
- [ ] **Step 5: Commit.** `fix(routing): a custom route's wildcard orders its certificate on dns01`

### Task 4: Wake catch-alls match any depth

**Files:**
- Modify: `packages/pstack/internal/initctl/init.go` (`WakeRouterLabels` ~:976-981)
- Modify: `packages/pstack/internal/routing/domains.go` (`renderDomains`, the `pstack-wake-` rule)
- Test: `packages/pstack/internal/initctl/init_test.go` (`TestInitSwarmModeAndWakeCatchAll`), `packages/pstack/internal/routing/domains_test.go` (`TestEachDomainGetsConsoleAPIAndAWakeRouter`), `packages/conformance/test/api-share-sleep-swarm.test.ts` (or whichever file asserts `x-pstack-wake`)
- Goldens: every `packages/conformance/golden/render/control/*/docker-compose.yml`, the `cli/init-dry-*` goldens, and the cloud-init goldens if they embed the label

**Interfaces:**
- Produces: the primary label `` traefik.http.routers.pstack-wake.rule=HostRegexp(`^([a-z0-9-]+\.)+<escaped domain>$$`) ``; the added-domain YAML rule `` "HostRegexp(`^([a-z0-9-]+\\.)+<escaped>$`)" ``. Priority stays 1.

- [ ] **Step 1: Write the failing tests.**
  - Both Go tests assert the new rule string.
  - Add a Go check that compiles the rule (with `$$` unescaped) and matches `web-pr-1.d`, `a.web-pr-1.d` and `a.b.web-pr-1.d`, but not `d` or `x.other.com`.
  - Conformance pin (not a failing test: the harness has no Traefik in front, and the API's sleep index already matches `-wild` rules, so this likely passes on today's binary; run it first and say so): a sleeping deployment with `subdomains: [web]` answers `Host: a.web-<stack>.<domain>` with 503 and `x-pstack-wake: 1`. Its negative control: drop the `-wild` rule from the sleep entry and the request is not woken.
- [ ] **Step 2: Run the Go tests and watch them fail.** The Go regexp check is the failing test for this task.
- [ ] **Step 3: Implement** the two rule strings. Then rebuild `bin/pstack`, run `bun gen/goldens.ts`, and check with `git diff --stat packages/conformance/golden` that only wake-rule lines moved.
- [ ] **Step 4: Run.** `go test -race ./internal/initctl/ ./internal/routing/` and `cd packages/conformance && bun test`.
- [ ] **Step 5: Add the CHANGELOG line** under `### Fixed`: "**Wake** catch-alls match any depth, so a sleeping stack's subdomain subtree wakes it. `pstack upgrade` applies it to the primary and the added domains." **Commit** it with the goldens: `fix(init): wake catch-alls match any depth under the domain`.

### Task 5: Docs and CHANGELOG

**Files:**
- Modify: `docs/usage.md` §5b (~:638-710)
  - Replace "pstack does not write your Traefik labels" and its hand-written label example: pstack generates the `-wild` router, and `${PSTACK_WILD_<P>}` is for a service with its own `traefik.*` labels.
  - Replace the DNS bullet: `*.<domain>` covers deeper names. Add the D8 empty-non-terminal caveat in two sentences.
  - Replace the TLS bullet with the per-mode table from D2–D4.
  - Add the custom-routes paragraph from the design.
- Modify: `docs/usage.md` "Custom routes" (~:1706): one sentence saying a stack's subdomains are mirrored on the alias, with the same TLS rules.
- Modify: `skills/pstack/SKILL.md`: a short `compose.subdomains` entry beside the routing labels.
- Modify: `packages/pstack/CHANGELOG.md` `## Unreleased`:
  - `### Fixed`:
    - **Wildcard subdomains** get a certificate on DNS-01 hosts (one per deployment, and one per custom route), and wake a sleeping stack.
    - Other modes note why there is none.
    - A subdomains host that is a served domain or a control host is refused.
    - Redeploy (`POST /api/tls/redeploy`) to stamp existing deployments.

- [ ] **Step 1: Edit.**
- [ ] **Step 2: Check.** `bun run check` from the repo root passes; the docs build reports every link resolving.
- [ ] **Step 3: Commit.** `docs: subdomain subtrees — what pstack generates, DNS, and TLS by mode`

## Not verified by this plan

Every test asserts label strings or YAML; nothing runs a real Traefik or ACME. Before release, on
one DNS-01 host: deploy a `subdomains: [web]` spec, then check:
- `curl -v https://a.web-<stack>.<domain>` presents a certificate whose SANs include
  `*.web-<stack>.<domain>`;
- `acme.json` gained exactly one certificate;
- `a.web-<stack>.<domain>` on the same stack, put to sleep, wakes it.
