// Package initctl is `pstack init` — stand up the CONTROL stack on a host.
//
// ── WHY THIS LIVES IN THE CLI AND NOT IN THE API ──────────────────────────────────────────────
//
//	host (systemd / CLI)
//	  └─ pstack init ──▶ CONTROL stack: traefik + pstack (API + UI)
//	                                      │ manages
//	                                      ▼
//	                          shared + isolated deployments
//
// The running API manages every OTHER deployment. It must never manage the stack it is running
// in, because `up` on that project recreates the pstack container — killing the process halfway
// through the operation it was performing. The request never returns, the job transcript is lost
// with the process (job history is in-memory, invariant 10), and if the new image is broken the
// host is left with no control plane and no remote way to fix it: the only thing that could have
// repaired it was the thing that just died. So self-management is not a feature with a caveat,
// it is a way to brick a host from a browser tab.
//
// The host keeps that power. `init` runs over SSH, from systemd, or from CI-with-a-key — always
// from OUTSIDE the containers it is recreating, which is the only place a failed upgrade is
// recoverable.
//
// Everything here is idempotent: re-running `init` is the supported way to change the domain,
// rotate the token, or move to a new image.
package initctl

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/swarm"
)

// ControlProject is the control stack's compose project name. Exported because the API reads it to
// SHOW the control stack's status — never to act on it. Keep it stable: it is how an operator
// finds the stack with `docker compose -p pstack-control …`.
const ControlProject = "pstack-control"

// The two external Docker networks, matching `docs/bootstrap.md` §1 and the per-PR compose
// contract in §5. They answer different questions — `preview-ingress` is "who may Traefik route
// to", `preview-shared` is "who may reach shared infrastructure" — so a per-PR service that needs
// the database but must not be publicly routable joins only the second.
//
// These names are a CONTRACT with every per-PR compose file, which must declare both as
// `external: true`. Declare a different name on one side and Compose silently creates
// `pr-123_preview-ingress` instead: the container comes up healthy and is unreachable (§10).
// Renaming them here is therefore a breaking change to every spec on the host.
const (
	netIngress = "preview-ingress"
	netShared  = "preview-shared"
	socket     = "/var/run/docker.sock"
)

// Challenge is how Let's Encrypt verifies the domain.
type Challenge string

// UI is which web UI `control.<domain>` serves.
type UI string

const (
	HTTP01   Challenge = "http01"
	DNS01    Challenge = "dns01"
	Basic    UI        = "basic"
	Advanced UI        = "advanced"
)

// defaultImage is the image the control stack runs. Not an `init` option because it is a property
// of the *installation* (what you built or pulled), not of the host you are configuring; override
// with PSTACK_IMAGE when you publish the image somewhere.
func defaultImage() string {
	if v, ok := os.LookupEnv("PSTACK_IMAGE"); ok {
		return v
	}
	return "pstack:local"
}

// DNSTokenVar is lego's DNS-01 credential variable, per provider — the name Traefik expects the
// token under.
//
// ONLY the providers verified in `docs/bootstrap.md` §8 are listed. A wrong variable name fails
// as "propagation timeout", which sends you debugging DNS instead of a typo, so an unknown
// provider gets a CHANGEME line pointing at lego's list rather than a guess.
//
// "" (the TS's `null`) means the provider is *tokenless*: AWS satisfies the credential chain from
// an instance profile and GCP from the VM's attached service account, so there is nothing to
// write, nothing on disk, and nothing to rotate. Prefer those where you have the choice. Presence
// in the map is the "known" test; a lookup table, never ranged into output.
var DNSTokenVar = map[string]string{
	"cloudflare": "CF_DNS_API_TOKEN", // a single token with Zone:Read + DNS:Edit
	"hetzner":    "HETZNER_API_TOKEN",
	"route53":    "", // IAM instance profile
	"gcloud":     "", // Application Default Credentials
}

// Options are init's inputs.
type Options struct {
	// DataDir is where the control plane keeps its state — the same path the API reads as PSTACK_DATA.
	DataDir string
	// Domain is the apex of the preview domain, e.g. `preview.example.com`. The UI lands on `control.<domain>`.
	Domain string
	// AcmeEmail is the ACME registration address. Let's Encrypt mails expiry warnings here.
	AcmeEmail string
	// Challenge is how Let's Encrypt verifies the domain.
	//
	//   http01  DEFAULT. Zero credentials — Traefik answers a challenge on port 80. Works the
	//           moment DNS points at the box. CANNOT issue wildcards, so every hostname gets its
	//           own certificate, and Let's Encrypt allows only ~50 new certificates per registered
	//           domain per week. At 3 surfaces per PR that is ~16 PRs/week. It also cannot cover a
	//           hostname before its container exists, so a not-yet-deployed preview URL has no cert.
	//   dns01   One wildcard covers every present and future `*.<domain>` host: no per-PR issuance,
	//           no rate-limit ceiling, and a URL is valid before the stack is deployed. Costs a DNS
	//           API credential to obtain, store and rotate.
	//
	// Start on http01; move to dns01 when PR volume or pre-deploy URLs demand it.
	Challenge Challenge
	// UI is which web UI `control.<domain>` serves.
	//
	//   basic     DEFAULT. The UI embedded in the API bundle — no extra container, nothing else to
	//             build or pull. Enough to submit a deployment and watch a job.
	//   advanced  The standalone SPA (@samyx/preview-stacks-ui) as its OWN container, which the
	//             control stack gains a service for. Richer, but one more image to build and keep
	//             current, so it is opt-in rather than the default.
	//
	// Either way the API keeps serving the basic UI on `api.<domain>`, so a broken advanced image
	// never leaves the host with no usable interface.
	UI UI
	// Orchestrator is how previews are deployed on this host.
	//
	//   swarm    DEFAULT for a new host. This daemon becomes a one-node swarm manager; previews deploy
	//            as swarm stacks; workers can join later from the swarm panel. The control stack
	//            itself stays a plain compose project on the manager (it must never depend on the
	//            orchestrator it manages), and the two external networks become attachable overlays
	//            so its containers reach tasks on any node.
	//   compose  What every host before 0.26.0 ran. `pstack upgrade` keeps it: switching an existing
	//            host means recreating its networks, which needs every preview torn down first.
	Orchestrator spec.Orchestrator
	// DNSProvider is the lego DNS-01 provider code, e.g. `cloudflare`. Required for, and only used by, `dns01`.
	DNSProvider string
	// Token is the DNS-01 API token, and ONLY that — it is written to `dns.env` for Traefik and
	// never reused as the API's bearer token. Two different secrets, two different blast radii:
	// this one can edit a DNS zone, PSTACK_TOKEN can start privileged containers. Omit it for the
	// tokenless providers (route53, gcloud), which authenticate from the instance's own identity.
	Token string
	// DryRun: change nothing — print what would happen. Must match the flag the runner was built with.
	DryRun bool
	// Runner is from exec.New, so `--dry-run` behaves exactly as it does everywhere else.
	Runner exec.Runner
	// Out is where init's own lines go (stdout when nil). Pass the runner's Out so a dry-run
	// transcript interleaves in order.
	Out io.Writer
}

// randomToken is 24 random bytes, hex. Hex on purpose: `$` in a `.env` value is expanded by Compose.
func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// healthPoll is the wait between health probes. A var so a test may shorten it.
var healthPoll = 2 * time.Second

// Init stands up the control stack.
func Init(opts Options) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	dataDir, domain, acmeEmail, dnsProvider := opts.DataDir, opts.Domain, opts.AcmeEmail, opts.DNSProvider
	challenge, ui, orchestrator, dryRun, runner := opts.Challenge, opts.UI, opts.Orchestrator, opts.DryRun, opts.Runner
	image := defaultImage()
	uiImage := "pstack-ui:local"
	if v, ok := os.LookupEnv("PSTACK_UI_IMAGE"); ok {
		uiImage = v
	}
	controlDir := filepath.Join(dataDir, "control")
	composePath := filepath.Join(controlDir, "docker-compose.yml")

	// The API's bearer token — deliberately NOT opts.Token, which is the DNS credential. Reusing
	// one secret for both would give whoever can edit a DNS zone the ability to start privileged
	// containers on this host.
	//
	// Generated when unset rather than left empty: the control stack binds 0.0.0.0 so Traefik can
	// reach it, and `serve` hard-exits (3) on a non-loopback bind with no PSTACK_TOKEN. An
	// unauthenticated API holding a read-write Docker socket must not be reachable, ever. Supply
	// your own through the environment (`PSTACK_TOKEN=… pstack init`) to keep an existing one.
	// `||` in the TS: an empty PSTACK_TOKEN counts as unset (rule 11).
	pstackToken := os.Getenv("PSTACK_TOKEN")
	generated := pstackToken == ""
	if generated {
		pstackToken = randomToken()
	}

	// ── 0. Preconditions ────────────────────────────────────────────────────────────────────────
	// Same shape and same reason as a spec's `requires:` — fail immediately, by name, before
	// anything is created, rather than deep inside a compose error nobody can read.
	// NOTE: under --dry-run these are skipped and report ok, like every other command. A green
	// dry-run proves ordering, never absence (AGENTS.md).
	type req struct{ name, assert, hint string }
	reqs := []req{
		{
			name:   "docker socket",
			assert: "test -S " + socket,
			hint:   "no Docker socket at " + socket + " — install Docker Engine first (docs/bootstrap.md §4).",
		},
		{
			name:   "compose plugin",
			assert: "docker compose version >/dev/null 2>&1",
			hint:   "the Compose v2 plugin is missing — install docker-compose-plugin.",
		},
		{
			name:   "control image",
			assert: "docker image inspect " + swarm.Shq(image) + " >/dev/null 2>&1",
			// Point at the command that needs nothing but this install. Telling an operator to clone
			// the source is what made a global install a dead end in the first place.
			hint: "image " + image + " not found — build it from this install: `pstack build-image`" +
				tagFlag(image, "pstack:local") +
				" (or pull it and pass PSTACK_IMAGE=<registry>/<image>)",
		},
	}
	// Only when the advanced UI is selected. Compose does not fail-fast on a missing image: it
	// tries to PULL `pstack-ui:local`, gets "pull access denied", and takes the WHOLE control stack
	// down with it — Traefik included. So an optional UI turned into a dead host. Check it here,
	// where the message can name the command that fixes it.
	if ui == Advanced {
		reqs = append(reqs, req{
			name:   "advanced UI image",
			assert: "docker image inspect " + swarm.Shq(uiImage) + " >/dev/null 2>&1",
			hint: "image " + uiImage + " not found — build it with `pstack build-image --ui`" +
				tagFlag(uiImage, "pstack-ui:local") + ", " +
				"or re-run without --ui advanced to use the basic UI embedded in the API.",
		})
	}
	for _, r := range reqs {
		res := runner.Run(r.assert, exec.RunOptions{Label: "requires " + r.name})
		if !res.OK {
			return errors.New(r.name + ": " + r.hint)
		}
	}

	// ── 1. State directories ────────────────────────────────────────────────────────────────────
	// `deployments/` is the Registry's root (it joins dataDir + 'deployments'); `traefik-dynamic/`
	// is the file provider's watched directory, created empty so the mount does not fail.
	if err := ensureDir(out, filepath.Join(dataDir, "deployments"), dryRun, noMode); err != nil {
		return err
	}
	if err := ensureDir(out, filepath.Join(controlDir, "traefik-dynamic"), dryRun, noMode); err != nil {
		return err
	}
	// DOCKER_CONFIG for the API's docker client. 0700 because config.json holds registry credentials as
	// reversible base64 — see registries.
	if err := ensureDir(out, filepath.Join(controlDir, "docker"), dryRun, 0o700); err != nil {
		return err
	}
	// The SQLite home (accounts, sessions, tokens). 0700 for the same reason as the docker config:
	// WAL siblings carry live data, so the directory is the permission boundary, not the file.
	if err := ensureDir(out, filepath.Join(dataDir, "db"), dryRun, 0o700); err != nil {
		return err
	}

	// ── 1b. Swarm ───────────────────────────────────────────────────────────────────────────────
	// `swarm init` on a node that is already a manager fails, so it is guarded by the state docker
	// reports — this is the idempotent path. Leaving a swarm is never done here: that is a decision
	// with workers attached to it, and it belongs to an operator at a shell.
	if orchestrator == spec.Swarm {
		state := runner.Run("docker info --format '{{.Swarm.LocalNodeState}}'", exec.RunOptions{Label: "swarm state"})
		if strings.TrimSpace(state.Stdout) != "active" {
			r := runner.Run("docker swarm init", exec.RunOptions{Label: "swarm init"})
			if !r.OK {
				return errors.New("docker swarm init failed:\n" + exec.Indent(firstOf(r.Stderr, r.Stdout)) + "\n" +
					"  A host with several addresses needs --advertise-addr; run `docker swarm init --advertise-addr <ip>` " +
					"once by hand, then re-run pstack init.")
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
	driver, createArgs := "bridge", ""
	if orchestrator == spec.Swarm {
		driver, createArgs = "overlay", "-d overlay --attachable "
	}
	for _, net := range []string{netIngress, netShared} {
		have := runner.Run("docker network inspect -f '{{.Driver}}' "+net+" 2>/dev/null", exec.RunOptions{Label: "network " + net + " driver"})
		current := ""
		if have.OK {
			current = strings.TrimSpace(have.Stdout)
		}
		// Only a driver docker could have meant. Anything else is "could not tell", and a network that
		// cannot be read is not one to remove.
		if (current == "bridge" || current == "overlay") && current != driver {
			attached := runner.Run("docker network inspect -f '{{range .Containers}}{{.Name}} {{end}}' "+net,
				exec.RunOptions{Label: "network " + net + " members"})
			var foreign []string
			for _, n := range strings.Fields(attached.Stdout) {
				if !strings.HasPrefix(n, ControlProject+"-") {
					foreign = append(foreign, n)
				}
			}
			if len(foreign) > 0 {
				keep := "compose"
				if current == "overlay" {
					keep = "swarm"
				}
				return fmt.Errorf("network %s is a %s network and must become %s for --orchestrator %s, "+
					"but these containers are attached to it: %s.\n"+
					"  Tear those previews down (pstack down / the API), then re-run init. Or keep the host as it "+
					"is with --orchestrator %s.", net, current, driver, orchestrator, strings.Join(foreign, ", "), keep)
			}
			// Only the control stack is on it: take that down (it is recreated below anyway), swap the network.
			runner.Run("docker compose -p "+ControlProject+" down 2>/dev/null || true", exec.RunOptions{Label: "control stack down (network swap)"})
			rm := runner.Run("docker network rm "+net, exec.RunOptions{Label: "network " + net + " rm"})
			if !rm.OK {
				return errors.New("could not replace network " + net + ":\n" + exec.Indent(firstOf(rm.Stderr, rm.Stdout)))
			}
		}
		runner.Run("docker network create "+createArgs+net+" 2>/dev/null || true", exec.RunOptions{Label: "network " + net})
	}

	// ── 3. Configuration ────────────────────────────────────────────────────────────────────────
	// The template's `${...}` placeholders are *Compose's* own interpolation, resolved from `.env` at
	// `up` time — never substituted here (invariant 6: exactly one interpolator, in spec).
	//
	// The two `#__MARKER__` lines are different: Compose cannot conditionally include CLI arguments,
	// and the ACME challenge changes both Traefik's flags and the router's TLS labels. So init
	// renders those two blocks and leaves everything else byte-for-byte.
	// strings.Replace with n=1, never regexp (rule 8): the TS used function replacements because
	// String.replace reads `$$` in a string replacement as one `$`, and the wake router's rule ends
	// in `$$` precisely so compose hands Traefik a literal `$`. Here `$` means nothing to
	// strings.Replace and everything to regexp — same invariant (18), inverted mechanism.
	template := pstack.ControlTemplate
	template = strings.Replace(template, "      #__ACME_CHALLENGE__", AcmeChallengeArgs(challenge, dnsProvider), 1)
	template = strings.Replace(template, "      #__ACME_ROUTER_TLS__", AcmeRouterLabels(challenge), 1)
	template = strings.Replace(template, "      #__CONTROL_UI_SERVICE__", ControlUIService(ui), 1)
	template = strings.Replace(template, "      #__SWARM_PROVIDER__", SwarmProviderArgs(orchestrator), 1)
	template = strings.Replace(template, "      #__WAKE_ROUTER__", WakeRouterLabels(domain), 1)
	template = strings.Replace(template, "#__ADVANCED_UI_SERVICE__", AdvancedUIService(ui), 1)
	if err := write(out, composePath, template, 0o644, dryRun); err != nil {
		return err
	}

	// 0600: this file holds PSTACK_TOKEN, and that token drives an API with a read-write Docker
	// socket — i.e. root on this host. Anyone who can read it owns the box.
	if err := write(out, filepath.Join(controlDir, ".env"), envFile(envValues{
		dataDir: dataDir, domain: domain, acmeEmail: acmeEmail, dnsProvider: dnsProvider, image: image,
		pstackToken: pstackToken, ui: ui, uiImage: uiImage, orchestrator: orchestrator,
	}), 0o600, dryRun); err != nil {
		return err
	}

	// Only meaningful for dns01; written either way so switching modes needs no extra step.
	if err := write(out, filepath.Join(controlDir, "dns.env"), dnsEnvFile(dnsProvider, opts.Token), 0o600, dryRun); err != nil {
		return err
	}

	// ── 4. Bring it up ──────────────────────────────────────────────────────────────────────────
	upCmd := "docker compose -p " + ControlProject + " -f " + swarm.Shq(composePath) + " up -d --remove-orphans"
	up := runner.Run(upCmd, exec.RunOptions{Label: "control stack up"})
	if !up.OK {
		return errors.New("control stack failed to start:\n" + exec.Indent(firstOf(up.Stderr, up.Stdout)))
	}

	// ── 5. Prove it ─────────────────────────────────────────────────────────────────────────────
	// `up -d` exits 0 as soon as the containers are *created*. A container that crash-loops still
	// gets you a zero here, so `init` would print success over a dead control plane. The image ships
	// a HEALTHCHECK against /api/health; wait for it and report what actually happened.
	if !dryRun {
		health := waitHealthy(runner)
		if health != "healthy" {
			return errors.New("control stack started but the API never became healthy (last state: " + health + ").\n" +
				"  docker compose -p " + ControlProject + " logs pstack\n" +
				"  docker compose -p " + ControlProject + " ps")
		}
	}

	// ── 6. What to do next ──────────────────────────────────────────────────────────────────────
	// control.<domain>, not the long-gone pstack.<domain> — the hostnames split when the API and UI
	// got their own routers.
	uiURL := "https://control." + domain
	apiURL := "https://api." + domain
	dry := ""
	if dryRun {
		dry = ", dry-run"
	}
	previews := "docker compose on this host"
	if orchestrator == spec.Swarm {
		previews = "docker stack deploy on this swarm (one manager; add workers from the Swarm page)"
	}
	lines := []string{
		"",
		"control stack up  (project " + ControlProject + dry + ")",
		"  config    " + composePath,
		"  registry  " + filepath.Join(dataDir, "deployments"),
		"  networks  " + netIngress + ", " + netShared + "   (" + driver + "; declare both as `external: true` in every per-PR compose file)",
		"  previews  " + previews,
	}
	if orchestrator == spec.Swarm {
		lines = append(lines, "            workers need "+swarm.PortList()+" open to and from this host")
	}
	envPath := filepath.Join(controlDir, ".env")
	lines = append(lines,
		"",
		"next:",
		"  1. UI          "+uiURL,
		"     DNS must already answer for "+domain+" and *."+domain+"; the wildcard certificate is",
		"     requested on first request and can take a minute (docs/bootstrap.md §10 if it never arrives).",
		"  2. Health      curl -sf "+apiURL+"/api/health",
		"  3. Submit      curl -X PUT "+apiURL+"/api/deployments/<id> \\",
		`                   -H "Authorization: Bearer $PSTACK_TOKEN" \`,
		"                   -H 'content-type: application/json' \\",
		`                   -d '{"spec": "<preview.yml>", "compose": "<docker-compose.yml>"}'`,
		"                 then POST /api/deployments/<id>/up. A spec with `stack: pr-${PR}` needs",
		"                 its variables on every call (…?PR=7) — and the SAME ones on `down`, or",
		"                 teardown targets a different stack than deploy created. Full route list:",
		"                 the header of src/api.ts. Or just use the UI.",
		"  4. Upgrade     pstack upgrade  — reads this .env so the token, domain and challenge",
		"                 mode all survive. Never from inside the control stack; SSH in.",
		"  5. Rotate      PSTACK_TOKEN=<new> pstack init …  — re-running rewrites .env and recreates",
		"                 the container. Omit it entirely to be handed a fresh one. The only copy",
		"                 lives in "+envPath+" (0600); the DNS credential is a",
		"                 separate secret in dns.env and is not affected.",
		"",
	)
	if generated {
		lines = append(lines, "  PSTACK_TOKEN="+pstackToken+"\n  ^ generated — this is the ONLY time it is printed. Store it now.")
	} else {
		lines = append(lines, "  PSTACK_TOKEN: taken from the environment (stored 0600 in "+envPath+")")
	}
	lines = append(lines, "")
	fmt.Fprintln(out, strings.Join(lines, "\n"))
	return nil
}

// tagFlag is the ` --tag <image>` suffix a hint carries when the image is not the default.
func tagFlag(image, def string) string {
	if image == def {
		return ""
	}
	return " --tag " + image
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// waitHealthy polls the pstack container's HEALTHCHECK. Asking Compose for the id rather than
// assuming `<project>-<service>-1` keeps this correct if the container is renamed or scaled.
//
// Bounded at ~60s: the health check itself has a start period, and a control plane that needs
// longer than a minute to answer its own liveness route has something to say in its logs.
func waitHealthy(runner exec.Runner) string {
	probe := "cid=$(docker compose -p " + ControlProject + " ps -q pstack) && [ -n \"$cid\" ] && " +
		"docker inspect -f '{{.State.Health.Status}}' \"$cid\""
	last := "unknown"
	for i := 0; i < 30; i++ {
		r := runner.Run(probe, exec.RunOptions{Label: "health"})
		if s := strings.TrimSpace(r.Stdout); s != "" {
			last = s
		}
		if last == "healthy" {
			return last
		}
		// `unhealthy` is only reported after the start period AND the retry budget, so it is a verdict,
		// not a transient — waiting longer just delays the same error.
		if last == "unhealthy" {
			return last
		}
		time.Sleep(healthPoll)
	}
	return last
}

type envValues struct {
	ui           UI
	uiImage      string
	orchestrator spec.Orchestrator
	dataDir      string
	domain       string
	acmeEmail    string
	dnsProvider  string
	image        string
	pstackToken  string
}

// envFile is the values every `${...}` in the compose template reads. Compose loads this from `.env`.
func envFile(v envValues) string {
	uiImage := v.uiImage
	if uiImage == "" {
		uiImage = "pstack-ui:local"
	}
	return strings.Join([]string{
		"# Written by `pstack init`. Compose reads this file from the project directory at `up` time.",
		"# Re-run `pstack init` to change anything here rather than editing by hand — the two must agree.",
		"#",
		"# Compose expands `${...}` inside these values. A literal `$` must be written `$$`.",
		"",
		"DATA_DIR=" + v.dataDir,
		"DOMAIN=" + v.domain,
		// Only meaningful with --ui advanced; harmless otherwise, and having it present means
		// switching modes is a re-run of `init` rather than an .env edit.
		"PSTACK_UI_IMAGE=" + uiImage,
		"ACME_EMAIL=" + v.acmeEmail,
		"DNS_PROVIDER=" + v.dnsProvider,
		"PSTACK_IMAGE=" + v.image,
		"# How previews deploy: swarm (docker stack deploy) or compose. `pstack upgrade` keeps it.",
		"PSTACK_ORCHESTRATOR=" + string(v.orchestrator),
		"",
		"# Bearer token for the API. Mutating routes require it; GETs do not, so ingress auth is still",
		"# the only gate on job transcripts and the stack list (docs/bootstrap.md §9).",
		"PSTACK_TOKEN=" + v.pstackToken,
		"",
	}, "\n")
}

// AcmeChallengeArgs is Traefik's ACME challenge flags for the chosen mode, at the template's indentation.
//
// HTTP-01 needs no credential: Traefik answers on the `web` entrypoint over port 80. The
// web→websecure redirect does NOT break it — Traefik installs an internal ACME router at maximum
// priority that bypasses the redirect for `/.well-known/acme-challenge/`. Port 80 must be reachable
// from the internet, which is the one hard requirement.
func AcmeChallengeArgs(challenge Challenge, provider string) string {
	if challenge == HTTP01 {
		return strings.Join([]string{
			"      - --certificatesresolvers.le.acme.httpchallenge=true",
			"      # Must be the :80 entrypoint, and :80 must be reachable from the internet.",
			"      - --certificatesresolvers.le.acme.httpchallenge.entrypoint=web",
		}, "\n")
	}
	// (The TS appended `provider ? '' : ''` — nothing either way; the provider reaches Traefik via
	// ${DNS_PROVIDER} in .env.)
	return strings.Join([]string{
		"      - --certificatesresolvers.le.acme.dnschallenge=true",
		"      - --certificatesresolvers.le.acme.dnschallenge.provider=${DNS_PROVIDER}",
		"      # Ask the authoritative resolvers directly so a stale local cache cannot fail the",
		"      # propagation check.",
		"      - --certificatesresolvers.le.acme.dnschallenge.resolvers=1.1.1.1:53",
		"      - --certificatesresolvers.le.acme.dnschallenge.propagation.delaybeforechecks=30s",
	}, "\n")
}

// AcmeRouterLabels is the TLS labels on the control router.
//
// dns01 requests ONE wildcard from a single always-on router; every other router (including every
// per-PR one) sets `tls=true` alone and inherits it by SNI. Adding `certresolver` to another router
// makes Traefik order a SEPARATE certificate for it — the rate-limit trap.
//
// http01 cannot do wildcards at all, so each router resolves its own certificate on first request.
func AcmeRouterLabels(challenge Challenge) string {
	if challenge == HTTP01 {
		return strings.Join([]string{
			"      # HTTP-01 cannot issue wildcards, so each hostname resolves its own certificate on its",
			"      # first HTTPS request. Per-PR routers therefore need `tls.certresolver=le` too — unlike",
			"      # the dns01 setup, where they must NOT have it.",
			"      - traefik.http.routers.pstack-ui.tls=true",
			"      - traefik.http.routers.pstack-ui.tls.certresolver=le",
			"      - traefik.http.routers.pstack-api.tls=true",
			"      - traefik.http.routers.pstack-api.tls.certresolver=le",
		}, "\n")
	}
	return strings.Join([]string{
		"      # THE ONE ROUTER THAT REQUESTS THE WILDCARD. Every other router — including every per-PR",
		"      # service — sets `tls=true` and NOTHING else, inheriting this certificate by SNI. Adding",
		"      # `certresolver` to a per-PR router orders a separate certificate and burns the",
		"      # ~50-per-week limit. The wildcard matches ONE label: `backend-pr-1.${DOMAIN}` is covered,",
		"      # `backend.pr-1.${DOMAIN}` is not — flatten per-PR hostnames with dashes.",
		"      - traefik.http.routers.pstack-ui.tls=true",
		"      - traefik.http.routers.pstack-ui.tls.certresolver=le",
		"      - traefik.http.routers.pstack-ui.tls.domains[0].main=${DOMAIN}",
		"      - traefik.http.routers.pstack-ui.tls.domains[0].sans=*.${DOMAIN}",
		"      - traefik.http.routers.pstack-api.tls=true",
	}, "\n")
}

// SwarmProviderArgs is Traefik's swarm provider, in swarm mode only. Both providers run: docker for
// the control stack's own containers (and any compose-mode preview), swarm for the stacks.
// `exposedbydefault=false` on both for the same reason — routing is opt-in.
func SwarmProviderArgs(orchestrator spec.Orchestrator) string {
	if orchestrator != spec.Swarm {
		return "      # (compose mode: no swarm provider)"
	}
	return strings.Join([]string{
		"      - --providers.swarm=true",
		"      - --providers.swarm.exposedbydefault=false",
		"      - --providers.swarm.network=preview-ingress",
	}, "\n")
}

// WakeRouterLabels is the catch-all router for wake-on-call. A Go regexp over one label under the
// domain; `$$` is how a literal `$` survives compose's interpolation of this file, and the dots are
// escaped here (the template cannot, it only knows `${DOMAIN}`).
func WakeRouterLabels(domain string) string {
	re := spec.EscapeHostRegexp(domain)
	return strings.Join([]string{
		"      - traefik.http.routers.pstack-wake.rule=HostRegexp(`^[a-z0-9-]+\\." + re + "$$`)",
		"      - traefik.http.routers.pstack-wake.priority=1",
		"      - traefik.http.routers.pstack-wake.entrypoints=websecure",
		"      - traefik.http.routers.pstack-wake.tls=true",
		"      - traefik.http.routers.pstack-wake.service=pstack",
	}, "\n")
}

// ControlUIService is which Traefik service `control.<domain>` routes to.
//
// The ROUTER stays on the pstack container in both modes so the TLS labels render identically;
// only its target changes. Traefik happily lets a router declared on one container reference a
// loadbalancer service declared on another, which keeps this a one-line swap.
func ControlUIService(ui UI) string {
	if ui == Advanced {
		return strings.Join([]string{
			"      # Advanced UI selected: control.<domain> serves the SPA container below. The API",
			"      # still serves the basic UI on api.<domain>, so a broken UI image never leaves this",
			"      # host without an interface.",
			"      - traefik.http.routers.pstack-ui.service=advanced-ui",
		}, "\n")
	}
	return "      - traefik.http.routers.pstack-ui.service=pstack"
}

// AdvancedUIService is the optional advanced-UI container. Omitted entirely in basic mode — not
// merely disabled.
func AdvancedUIService(ui UI) string {
	if ui != Advanced {
		return ""
	}
	return strings.Join([]string{
		"",
		"  # The opt-in advanced UI (`pstack init --ui advanced`). A static SPA behind nginx, which",
		"  # proxies /api/ back to the pstack container so the browser stays same-origin and needs no",
		"  # CORS. Build it with `pstack build-image --ui`.",
		"  advanced-ui:",
		"    image: ${PSTACK_UI_IMAGE}",
		"    restart: unless-stopped",
		"    mem_limit: 128m",
		"    depends_on: [pstack]",
		"    networks: [preview-ingress]",
		"    labels:",
		"      - traefik.enable=true",
		"      - traefik.docker.network=preview-ingress",
		"      # No router here: control.<domain> is declared on the pstack container (so the TLS",
		"      # labels stay in one place) and points at this service by name.",
		"      - traefik.http.services.advanced-ui.loadbalancer.server.port=80",
		"",
	}, "\n")
}

// dnsEnvFile is the DNS-01 credential, in its own file because Compose cannot build an environment
// key from a variable — and every provider names its token differently. `env_file:` is the native
// answer: arbitrary keys, one file, no interpolation of the key itself.
func dnsEnvFile(provider, token string) string {
	head := []string{
		"# DNS-01 credential for Traefik (lego). Written by `pstack init`, mode 0600.",
		"# Every lego variable also accepts a `_FILE` suffix if you would rather mount the secret.",
		"# Provider list + exact variable names: https://go-acme.github.io/lego/dns/",
		"",
	}
	varName, known := DNSTokenVar[provider]

	if known && varName == "" {
		return strings.Join(append(head,
			`# provider "`+provider+`" is tokenless: it authenticates from the instance's own identity`,
			"# (AWS instance profile / GCP attached service account). Nothing to store, nothing to rotate.",
			"",
		), "\n")
	}
	if !known {
		return strings.Join(append(head,
			`# provider "`+provider+`" is not one of the names verified in docs/bootstrap.md §8, so the`,
			"# variable it expects is NOT guessed here — a wrong name surfaces as an ACME \"propagation",
			"# timeout\", which sends you debugging DNS instead of this line.",
			"#",
			"# Look it up in lego's provider list above and replace CHANGEME_VARIABLE_NAME:",
			"CHANGEME_VARIABLE_NAME="+token,
			"",
		), "\n")
	}
	return strings.Join(append(head, varName+"="+token, ""), "\n")
}

// ensureDir is mkdir -p, or says what it would have made. A dry run that creates directories is
// not a dry run. mode < 0 means "no explicit mode".
func ensureDir(out io.Writer, path string, dryRun bool, mode os.FileMode) error {
	if dryRun {
		fmt.Fprintf(out, "  [dry-run] mkdir -p %s\n", path)
		return nil
	}
	if err := os.MkdirAll(path, 0o777); err != nil {
		return err
	}
	// Applied separately: `mkdir`'s mode is masked by the process umask, so a 0700 request can land as
	// 0755 — and this directory holds registry credentials.
	if mode != noMode {
		_ = os.Chmod(path, mode)
	}
	return nil
}

// noMode is ensureDir's "leave it to the umask" (the TS passed no mode argument).
const noMode os.FileMode = 1<<32 - 1

// write writes a file at an exact mode.
//
// `chmod` is separate and unconditional because a create-time mode applies only when the file is
// CREATED. `init` is meant to be re-run, so on the second pass a `.env` that someone loosened to
// 0644 would silently stay 0644 — the one file where that matters most.
func write(out io.Writer, path, body string, mode os.FileMode, dryRun bool) error {
	if dryRun {
		// `body.length` — UTF-16 units, not bytes: the compose file carries `—` and `═`.
		fmt.Fprintf(out, "  [dry-run] write %s (%d bytes, mode %s)\n", path, js.Len(body), strconv.FormatUint(uint64(mode), 8))
		return nil
	}
	if err := os.WriteFile(path, []byte(body), 0o666); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
