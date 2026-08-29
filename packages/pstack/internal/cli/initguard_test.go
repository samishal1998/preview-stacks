package cli

import (
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/initctl"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/upgrade"
)

// A host with something to lose on every axis: a token, a DNS credential, dns01, a provider, the
// advanced UI and swarm.
func richHost() *upgrade.ControlState {
	return &upgrade.ControlState{
		Token:        "the-existing-token",
		Domain:       "preview.example.com",
		AcmeEmail:    "ops@example.com",
		DNSProvider:  "cloudflare",
		DNSToken:     "the-dns-token",
		Challenge:    initctl.Challenge("dns01"),
		UI:           initctl.UI("advanced"),
		Orchestrator: spec.Orchestrator("swarm"),
	}
}

// bareInit is what `pstack init --domain … --acme-email …` parses to: every other flag at its
// DEFAULT, and nothing recorded as typed.
func bareInit() *Parsed {
	return &Parsed{
		Domain: "preview.example.com", AcmeEmail: "ops@example.com",
		Challenge: "http01", UI: "basic", Orchestrator: "swarm",
		Typed: map[string]bool{},
	}
}

func TestABareReInitIsRefusedForEveryThingItWouldSilentlyChange(t *testing.T) {
	// negative control: drop the `!typed(...)` conditions and compare values only — a deliberate
	// `--challenge dns01` on an http01 host is then refused too, and the documented way to switch
	// modes stops working. The guard has to distinguish an OMISSION from a decision.
	got := initReverts(richHost(), "/var/lib/pstack", bareInit(), false, false)
	if len(got) != 5 {
		t.Fatalf("expected every axis with something to lose, got %d: %+v", len(got), got)
	}
	msg := initRevertRefusal(got)
	for _, want := range []string{
		"the machine token",     // 401s every CI job holding the old one
		"the DNS-01 credential", // dns.env blanked; renewal fails weeks later
		"the ACME challenge",    // dns01 → http01 burns the weekly limit
		"the DNS provider",      // cloudflare → none
		"the web UI",            // advanced → basic deletes the SPA container
		"pstack upgrade -n",     // the command that prints the host's own line
		"--force",               // the way through, named
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must name %q:\n%s", want, msg)
		}
	}
	// The orchestrator matches (swarm is both the host's and the default), so it is NOT listed —
	// the guard reports what would change, not every axis it knows about.
	if strings.Contains(msg, "the orchestrator") {
		t.Errorf("an unchanged axis must not be reported:\n%s", msg)
	}
}

func TestWhatYouSpelledIsADecisionAndPassesThrough(t *testing.T) {
	// negative control: ignore the Typed map — every explicit flag becomes a refusal, `pstack
	// upgrade` (which passes all of them) stops working, and switching modes becomes impossible
	// without --force.
	p := bareInit()
	p.Challenge, p.UI, p.Orchestrator, p.DNSProvider = "http01", "basic", "compose", ""
	for _, f := range []string{"--challenge", "--ui", "--orchestrator", "--dns-provider"} {
		p.Typed[f] = true
	}
	// Every value differs from the host, and every one was asked for: nothing to refuse but the
	// two credentials, which have no flag to spell.
	got := initReverts(richHost(), "/var/lib/pstack", p, true, true)
	if len(got) != 0 {
		t.Fatalf("a spelled flag is a decision: %+v", got)
	}
	if initRevertRefusal(got) != "" {
		t.Error("no reverts means no refusal")
	}
}

func TestTheGuardIsSilentOnAFirstInitAndOnAnUnchangedRerun(t *testing.T) {
	// negative control: return a revert for a nil state — the FIRST init on a new host is refused,
	// which makes the product unusable out of the box.
	if got := initReverts(nil, "/var/lib/pstack", bareInit(), false, false); len(got) != 0 {
		t.Fatalf("a host that does not exist yet has nothing to lose: %+v", got)
	}
	// An upgrade supplies everything it read back, so the values match and the env is set.
	same := &upgrade.ControlState{
		Token: "t", Domain: "preview.example.com", Challenge: initctl.Challenge("http01"),
		UI: initctl.UI("basic"), Orchestrator: spec.Orchestrator("swarm"),
	}
	if got := initReverts(same, "/var/lib/pstack", bareInit(), true, false); len(got) != 0 {
		t.Fatalf("a re-run that changes nothing must pass: %+v", got)
	}
}

func TestTheTokenIsJudgedOnPresenceNotEmptiness(t *testing.T) {
	// negative control: test the token with `!= ""` instead of the env's PRESENCE — an operator who
	// deliberately exports an empty PSTACK_TOKEN is told they are minting a new one when they asked
	// for exactly what they typed (rule 11: `??` semantics, presence decides).
	host := &upgrade.ControlState{Token: "t", Challenge: initctl.Challenge("http01"), UI: initctl.UI("basic"), Orchestrator: spec.Orchestrator("swarm")}
	if got := initReverts(host, "/var/lib/pstack", bareInit(), true, false); len(got) != 0 {
		t.Errorf("a SET token, even empty, is the caller's choice: %+v", got)
	}
	if got := initReverts(host, "/var/lib/pstack", bareInit(), false, false); len(got) != 1 {
		t.Errorf("an UNSET token would mint a new one and must be refused: %+v", got)
	}
}
