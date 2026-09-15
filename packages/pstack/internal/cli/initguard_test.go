package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
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
		Challenge: "http01", UI: "basic", Orchestrator: "swarm", Logging: "none",
		Typed: map[string]bool{},
	}
}

func TestABareReInitIsRefusedForEveryThingItWouldSilentlyChange(t *testing.T) {
	// negative control: drop the `!typed(...)` conditions and compare values only — a deliberate
	// `--challenge dns01` on an http01 host is then refused too, and the documented way to switch
	// modes stops working. The guard has to distinguish an OMISSION from a decision.
	got := initReverts(richHost(), "/var/lib/pstack", bareInit(), false, false, false)
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
	got := initReverts(richHost(), "/var/lib/pstack", p, true, true, false)
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
	if got := initReverts(nil, "/var/lib/pstack", bareInit(), false, false, false); len(got) != 0 {
		t.Fatalf("a host that does not exist yet has nothing to lose: %+v", got)
	}
	// An upgrade supplies everything it read back, so the values match and the env is set.
	same := &upgrade.ControlState{
		Token: "t", Domain: "preview.example.com", Challenge: initctl.Challenge("http01"),
		UI: initctl.UI("basic"), Orchestrator: spec.Orchestrator("swarm"),
	}
	if got := initReverts(same, "/var/lib/pstack", bareInit(), true, false, false); len(got) != 0 {
		t.Fatalf("a re-run that changes nothing must pass: %+v", got)
	}
}

func TestTheTokenIsJudgedOnPresenceNotEmptiness(t *testing.T) {
	// negative control: test the token with `!= ""` instead of the env's PRESENCE — an operator who
	// deliberately exports an empty PSTACK_TOKEN is told they are minting a new one when they asked
	// for exactly what they typed (rule 11: `??` semantics, presence decides).
	host := &upgrade.ControlState{Token: "t", Challenge: initctl.Challenge("http01"), UI: initctl.UI("basic"), Orchestrator: spec.Orchestrator("swarm")}
	if got := initReverts(host, "/var/lib/pstack", bareInit(), true, false, false); len(got) != 0 {
		t.Errorf("a SET token, even empty, is the caller's choice: %+v", got)
	}
	if got := initReverts(host, "/var/lib/pstack", bareInit(), false, false, false); len(got) != 1 {
		t.Errorf("an UNSET token would mint a new one and must be refused: %+v", got)
	}
}

func TestALokiHostRefusesDroppingLokiOrItsPassword(t *testing.T) {
	// A Loki host with nothing else to lose: the token env is set and every other axis matches the
	// defaults, so each count below is the Loki checks alone.
	host := func() *upgrade.ControlState {
		return &upgrade.ControlState{
			Token: "t", Challenge: initctl.Challenge("http01"), UI: initctl.UI("basic"), Orchestrator: spec.Orchestrator("swarm"),
			Logging: initctl.Loki, LokiPassword: "0123456789abcdef0123456789abcdef",
		}
	}

	t.Run("a bare re-run would remove Loki", func(t *testing.T) {
		// negative control: delete the `state.Logging == initctl.Loki` check — no revert, and the
		// re-run removes Loki from under every running container's pushes.
		got := initReverts(host(), "/var/lib/pstack", bareInit(), true, false, false)
		if len(got) != 1 {
			t.Fatalf("expected the Loki logging revert alone, got %d: %+v", len(got), got)
		}
		msg := initRevertRefusal(got)
		for _, want := range []string{"Loki logging", "loki  →  none", "keep it with:  --logging loki"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the refusal must name %q:\n%s", want, msg)
			}
		}
	})

	t.Run("keeping Loki without its password would mint a new one", func(t *testing.T) {
		// negative control: drop `!hasLokiPassword` from the password check — the run that carries
		// the password is refused too, and `pstack upgrade` on a Loki host stops working.
		p := bareInit()
		p.Logging, p.Typed["--logging"] = "loki", true
		got := initReverts(host(), "/var/lib/pstack", p, true, false, false)
		if len(got) != 1 || got[0].what != "the Loki push password" || !strings.Contains(got[0].fix, `echo "$LOKI_PUSH_PASSWORD"`) {
			t.Fatalf("expected the push password revert alone: %+v", got)
		}
		if got := initReverts(host(), "/var/lib/pstack", p, true, false, true); len(got) != 0 {
			t.Errorf("a SET password is the caller's choice: %+v", got)
		}
	})

	t.Run("a spelled --logging none is a decision", func(t *testing.T) {
		// negative control: drop `!typed("--logging")` — turning Loki off on purpose is refused, and
		// so is the init line `pstack logging off` runs.
		p := bareInit()
		p.Typed["--logging"] = true
		if got := initReverts(host(), "/var/lib/pstack", p, true, false, false); len(got) != 0 {
			t.Errorf("--logging none was asked for: %+v", got)
		}
	})
}

// The init lines `pstack upgrade` and `pstack logging off` run are guarded too — the child `pstack
// init` reads the same host — so a line that trips the guard breaks the command that wrote it,
// and no dry-run golden would show it. The host is generated by the REAL init, with docker faked.
func TestTheHostsOwnInitLinesPassTheGuard(t *testing.T) {
	const pw = "0123456789abcdef0123456789abcdef"
	t.Setenv("PSTACK_LOKI_PASSWORD", pw)
	dataDir := t.TempDir()
	if err := initctl.Init(initctl.Options{
		DataDir: dataDir, Domain: "preview.example.com", AcmeEmail: "ops@example.com",
		Challenge: initctl.HTTP01, UI: initctl.Basic, Orchestrator: spec.Compose, Logging: initctl.Loki,
		Runner: exec.NewFake(nil, "healthy"), Out: &bytes.Buffer{},
	}); err != nil {
		t.Fatal(err)
	}
	state, err := upgrade.ReadControlState(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Logging != initctl.Loki || state.LokiPassword != pw {
		t.Fatalf("the fixture is not a Loki host: %+v", state)
	}
	// passes parses a step's command the way the child would and runs the guard against this host.
	// Shq-quoted values keep their quotes under strings.Fields; the guard never reads them.
	passes := func(t *testing.T, step upgrade.Step) {
		t.Helper()
		words := strings.Fields(step.Cmd)
		if len(words) < 2 || words[0] != "pstack" || words[1] != "init" {
			t.Fatalf("not an init line: %q", step.Cmd)
		}
		p, ex := ParseArgs(words[1:], func(k string) (string, bool) { v, ok := step.Env[k]; return v, ok })
		if ex != nil {
			t.Fatalf("%q does not parse: %v", step.Cmd, ex)
		}
		_, hasToken := step.Env["PSTACK_TOKEN"]
		_, hasDNSToken := step.Env["PSTACK_DNS_TOKEN"]
		_, hasLokiPassword := step.Env["PSTACK_LOKI_PASSWORD"]
		if got := initReverts(state, dataDir, p, hasToken, hasDNSToken, hasLokiPassword); len(got) != 0 {
			t.Errorf("%q is refused on its own host:\n%s", step.Cmd, initRevertRefusal(got))
		}
	}

	t.Run("pstack upgrade", func(t *testing.T) {
		// negative control: drop the PSTACK_LOKI_PASSWORD line from upgrade's initEnv — the line is
		// refused as "the Loki push password".
		steps := upgrade.PlanUpgrade(upgrade.PlanArgs{Phase: upgrade.Resume, Target: "0.0.0", State: state})
		passes(t, steps[len(steps)-1])
	})

	t.Run("pstack logging off", func(t *testing.T) {
		// negative control: drop ` --logging none` from T4's SwitchLogging off step — the line is
		// refused as "Loki logging".
		changed, steps, err := upgrade.SwitchLogging(upgrade.SwitchLoggingOptions{
			DataDir: dataDir, Logging: initctl.LoggingNone, Runner: exec.NewFake(nil, "healthy"), Log: func(string) {},
		})
		if err != nil || !changed || len(steps) != 1 {
			t.Fatalf("changed=%v steps=%+v err=%v", changed, steps, err)
		}
		passes(t, steps[0])
	})
}
