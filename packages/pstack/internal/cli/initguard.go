// THE SILENT-REVERT GUARD on `pstack init`.
//
// `init` renders the control stack from its ARGUMENTS ALONE — it reads nothing back — so every flag
// left off takes that flag's DEFAULT, host-wide, without saying so. That is not a hypothetical: it
// is the single largest source of upgrade incidents this project has had.
//
//	· a run without PSTACK_TOKEN MINTS A NEW ONE, and every CI job holding the old one starts
//	  getting 401s. `pstack upgrade` exists because of this.
//	· a run without `--ui advanced` reverts an advanced host to the basic UI, deleting the SPA
//	  container.
//	· a run without PSTACK_DNS_TOKEN on a dns01 host rewrites `dns.env` BLANK, and the wildcard's
//	  renewal then fails weeks later, silently.
//	· a run without `--challenge dns01` flips a wildcard host to per-hostname issuance, which burns
//	  the weekly rate limit and cannot be undone by re-running.
//
// Every one of those is an OMISSION, never a wrong value: the operator did not ask for the change
// and is not told about it. So that is exactly what this refuses, and nothing else.
//
// ── WHAT IT DOES NOT DO ──────────────────────────────────────────────────────────────────────────
//
// A flag you SPELLED is a decision, and passes through untouched — `--challenge dns01` on an http01
// host is the documented way to switch modes. A first `init` has nothing to compare against. And
// `pstack upgrade` re-runs `init` with every value read back from the host, so it never trips this.
//
// The guard is here rather than in initctl because reading the host's current state lives in
// `upgrade`, which already imports initctl — the other direction would be a cycle.
package cli

import (
	"fmt"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade"
)

// revert is one thing this run would change without being asked to.
type revert struct {
	what string // what changes, in the operator's words
	from string
	to   string
	fix  string // the flag or variable that would have preserved it
}

// initReverts is what an `init` with these arguments would silently change about a host that
// already exists. Empty when nothing would change, when nothing is there yet, or when every change
// was asked for explicitly.
//
// `state` is the host as it is now; `typed` is which flags were spelled on the command line;
// `hasToken`/`hasDNSToken` are whether the two environment variables were SET (presence, not
// emptiness — an explicitly empty one is a choice).
func initReverts(state *upgrade.ControlState, dataDir string, p *Parsed, hasToken, hasDNSToken bool) []revert {
	if state == nil {
		return nil
	}
	out := []revert{}
	typed := func(flag string) bool { return p.Typed != nil && p.Typed[flag] }

	// The machine token. No flag — it travels in the environment, and an absent one is not "keep
	// what is there", it is "mint a new one".
	if !hasToken && state.Token != "" {
		out = append(out, revert{
			what: "the machine token", from: "the one this host has", to: "a NEWLY GENERATED one",
			fix: "PSTACK_TOKEN=$(. " + dataDir + "/control/.env; echo \"$PSTACK_TOKEN\")",
		})
	}
	// The DNS-01 credential, same shape. Only meaningful on a host that uses one.
	if !hasDNSToken && state.DNSToken != "" {
		out = append(out, revert{
			what: "the DNS-01 credential", from: "the one this host has", to: "EMPTY — renewals would fail later, silently",
			fix: "PSTACK_DNS_TOKEN=<the token>",
		})
	}
	if !typed("--challenge") && string(state.Challenge) != "" && initctl.Challenge(p.Challenge) != state.Challenge {
		out = append(out, revert{
			what: "the ACME challenge", from: string(state.Challenge), to: p.Challenge,
			fix: "--challenge " + string(state.Challenge),
		})
	}
	if !typed("--dns-provider") && state.DNSProvider != "" && p.DNSProvider != state.DNSProvider {
		out = append(out, revert{
			what: "the DNS provider", from: state.DNSProvider, to: orNone(p.DNSProvider),
			fix: "--dns-provider " + state.DNSProvider,
		})
	}
	if !typed("--ui") && string(state.UI) != "" && initctl.UI(p.UI) != state.UI {
		out = append(out, revert{
			what: "the web UI", from: string(state.UI), to: p.UI,
			fix: "--ui " + string(state.UI),
		})
	}
	if !typed("--orchestrator") && string(state.Orchestrator) != "" && spec.Orchestrator(p.Orchestrator) != state.Orchestrator {
		out = append(out, revert{
			what: "the orchestrator", from: string(state.Orchestrator), to: p.Orchestrator,
			fix: "--orchestrator " + string(state.Orchestrator),
		})
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// initRevertRefusal is the message for a run that would silently change a host, or "" when it would
// not. It names every change, the flag that preserves each, and the one command that prints the
// whole line — because composing it by hand is how these incidents happen in the first place.
func initRevertRefusal(reverts []revert) string {
	if len(reverts) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to re-init: this would change %d thing(s) about a host that already exists, and you did not ask for any of them.\n\n", len(reverts))
	for _, r := range reverts {
		fmt.Fprintf(&b, "  %s\n      %s  →  %s\n      keep it with:  %s\n", r.what, r.from, r.to, r.fix)
	}
	b.WriteString("\n`init` renders the control stack from its arguments alone — it reads nothing back — so a flag\n")
	b.WriteString("left off takes that flag's default, host-wide. Rather than composing the line by hand, ask the\n")
	b.WriteString("host for its own:\n\n")
	b.WriteString("      pstack upgrade -n | grep 'pstack init'\n\n")
	b.WriteString("Re-run that with only what you meant to change. To proceed anyway, add --force.")
	return b.String()
}
