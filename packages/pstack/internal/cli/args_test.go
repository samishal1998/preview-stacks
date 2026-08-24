package cli

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	stdio "io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
)

type cliGolden struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func golden(t *testing.T, name string) cliGolden {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testfacts.Golden(t), "cli", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var g cliGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func noEnv(string) (string, bool) { return "", false }

func TestUsageMatchesTheGolden(t *testing.T) {
	// negative control: change one character of the usage text — the byte comparison fails.
	g := golden(t, "help")
	if got := Usage("<VERSION>"); got != g.Stdout {
		t.Errorf("--help differs from golden/cli/help.json:\n%s", diffLines(g.Stdout, got))
	}
}

func TestUnknownCommandMatchesTheGolden(t *testing.T) {
	// negative control: drop the `Commands:` line — the golden fails.
	g := golden(t, "unknown-command")
	e := UnknownCommand("upgradee", "<VERSION>")
	if e.Code != g.Code || "pstack: "+e.Msg+"\n" != g.Stderr {
		t.Errorf("got code %d\n%s\nwant\n%s", e.Code, "pstack: "+e.Msg+"\n", g.Stderr)
	}
}

func TestFlagsAnywhereAndTheUIPeek(t *testing.T) {
	// negative control: make --ui always consume the next word — `build-image --ui --tag x` misparses.
	p, e := ParseArgs([]string{"down", "-n", "-v", "--set", "PR=7", "--set", "PR=8", "--set", "X=1", "--no-verify"}, noEnv)
	if e != nil {
		t.Fatal(e)
	}
	if p.Cmd != "down" || !p.DryRun || p.Level != "verbose" || !p.NoVerify || p.Overrides["PR"] != "8" || strings.Join(p.OverrideKeys, ",") != "PR,X" {
		t.Errorf("got %+v", p)
	}
	p, _ = ParseArgs([]string{"build-image", "--ui", "--tag", "x"}, noEnv)
	if !p.UIImage || p.Tag != "x" || p.UI != "basic" {
		t.Errorf("--ui as a switch: %+v", p)
	}
	p, _ = ParseArgs([]string{"init", "--ui", "advanced"}, noEnv)
	if p.UIImage || p.UI != "advanced" || !p.Typed["--ui"] {
		t.Errorf("--ui as a value: %+v", p)
	}
	if _, e := ParseArgs([]string{"init", "--ui", "fancy"}, noEnv); e == nil || e.Code != ExitUsage || e.Msg != `--ui must be basic or advanced, got "fancy"` {
		t.Errorf("bad --ui: %+v", e)
	}
	if _, e := ParseArgs([]string{"--bogus"}, noEnv); e == nil || e.Msg != "unknown flag --bogus" {
		t.Errorf("unknown flag: %+v", e)
	}
	if _, e := ParseArgs([]string{"validate", "--set", "=v"}, noEnv); e == nil || !strings.Contains(e.Msg, "KEY=VALUE") {
		t.Errorf("--set =v: %+v", e)
	}
	p, _ = ParseArgs([]string{"swarm", "join", "--format", "script"}, noEnv)
	if p.Cmd != "swarm" || p.Sub != "join" || p.Format != "script" {
		t.Errorf("sub: %+v", p)
	}
}

func TestEnvDefaultsUseTheRightNullishness(t *testing.T) {
	// negative control: read PSTACK_CHALLENGE with `get` instead of `or` — an empty value stops meaning http01.
	env := func(k string) (string, bool) {
		switch k {
		case "PSTACK_CHALLENGE", "PSTACK_ORCHESTRATOR", "PSTACK_UI":
			return "", true // set but empty: the `||` sites fall back
		case "PSTACK_DOMAIN":
			return "", true // the `??` site keeps the empty string
		case "PSTACK_IMAGE":
			return "custom:tag", true
		}
		return "", false
	}
	p, _ := ParseArgs([]string{"init"}, env)
	if p.Challenge != "http01" || p.Orchestrator != "swarm" || p.UI != "basic" || p.Tag != "custom:tag" || p.Domain != "" {
		t.Errorf("got %+v", p)
	}
}

func TestCloudInitCredentialFlagsTakeNoEnvDefault(t *testing.T) {
	// negative control: default APIToken with get("PSTACK_TOKEN", "") — the second block fails.
	p, e := ParseArgs([]string{"cloud-init", "--admin-user", "alice", "--admin-password", "pw1", "--api-token", "tok-1"}, noEnv)
	if e != nil {
		t.Fatal(e)
	}
	if p.AdminUser != "alice" || p.AdminPassword != "pw1" || p.APIToken != "tok-1" {
		t.Errorf("got %+v", p)
	}
	// An operator's shell holds PSTACK_TOKEN because it talks to a host that ALREADY EXISTS. Picking
	// it up here would bake that host's bearer token into a new host's user-data — where the provider
	// keeps it as instance metadata — without anyone asking for it. The admin pair follows it: same
	// decision, same blast radius.
	env := func(k string) (string, bool) {
		switch k {
		case "PSTACK_TOKEN", "PSTACK_ADMIN_USER", "PSTACK_ADMIN_PASSWORD":
			return "from-the-shell", true
		}
		return "", false
	}
	p, _ = ParseArgs([]string{"cloud-init"}, env)
	if p.APIToken != "" || p.AdminUser != "" || p.AdminPassword != "" {
		t.Errorf("an environment credential leaked into the render: %+v", p)
	}
}

// `cloud-init` is where the credentials are decided, so the two decisions it makes on its own — a
// generated admin password, and a refusal — are checked here rather than only in the transcripts.
func TestCloudInitDecidesTheAdminCredentials(t *testing.T) {
	run := func(t *testing.T, argv ...string) (*Exit, string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		base := []string{"cloud-init", "--domain", "preview.example.com", "--acme-email", "ops@example.com", "--password", "dashpw", "-y"}
		p, e := ParseArgs(append(base, argv...), noEnv)
		if e != nil {
			t.Fatal(e)
		}
		return cloudInit(p, IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut, Env: noEnv}), out.String(), errOut.String()
	}

	// negative control: drop the adminUser == "" guard in cloudInit — this returns nil and renders a
	// file with no account in it, exactly as if the flag had not been passed.
	e, _, _ := run(t, "--admin-password", "hunter2hunter2")
	if e == nil || e.Code != ExitUsage || !strings.Contains(e.Msg, "--admin-password needs --admin-user") {
		t.Errorf("a password with no account: %+v", e)
	}

	// Generated, not prompted, and not left empty: this password lives in instance metadata for the
	// life of the host, so it must be a value that exists nowhere else — and the ONE place it is ever
	// shown is here, since `init` on the host prints nothing.
	e, yaml, said := run(t, "--admin-user", "alice")
	if e != nil {
		t.Fatalf("got %+v", e)
	}
	m := regexp.MustCompile(`(?m)^  pstack admin:       alice / ([0-9a-f]{24})  \(generated\)$`).FindStringSubmatch(said)
	if m == nil {
		t.Fatalf("no generated admin line:\n%s", said)
	}
	if !strings.Contains(yaml, "PSTACK_ADMIN_USER='alice' PSTACK_ADMIN_PASSWORD='"+m[1]+"'") {
		t.Error("the rendered file carries a different password than the one printed")
	}
	if !strings.Contains(said, "It also carries the admin password.") {
		t.Error("the metadata warning does not name the admin password")
	}
}

func diffLines(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "line " + itoa(i+1) + ":\n want " + a + "\n got  " + b
		}
	}
	return "(no line differs)"
}

func itoa(i int) string { return strconv.Itoa(i) }

// ── `pstack pull config` / `pstack push config` ─────────────────────────────────────────────────

func TestConfigFlagsParse(t *testing.T) {
	// negative control: make `-i` a switch rather than a value flag — In stays empty and this fails.
	p, e := ParseArgs([]string{"push", "config", "-i", "export.sealed", "-y"}, noEnv)
	if e != nil {
		t.Fatal(e)
	}
	if p.Cmd != "push" || p.Sub != "config" || p.In != "export.sealed" || !p.Yes {
		t.Errorf("push: %+v", p)
	}
	p, _ = ParseArgs([]string{"pull", "config", "-o", "export.sealed"}, noEnv)
	if p.Cmd != "pull" || p.Sub != "config" || p.Out != "export.sealed" {
		t.Errorf("pull: %+v", p)
	}
	// --config must not be swallowed by --config-repo or --config-url; they are three flags.
	p, _ = ParseArgs([]string{"cloud-init", "--config", "a.sealed", "--config-url", "https://x/y", "--config-repo", "git@h:r.git"}, noEnv)
	if p.Config != "a.sealed" || p.ConfigURL != "https://x/y" || p.ConfigRepo != "git@h:r.git" {
		t.Errorf("cloud-init: %+v", p)
	}
	if !IsCommand("pull") || !IsCommand("push") || IsSpecCommand("pull") {
		t.Error("pull/push are commands, and neither reads a spec")
	}
}

// The remote these two talk to is new: nothing in this CLI addressed another pstack before, so
// PSTACK_API_URL is invented here and every refusal below is a decision about what a typo may cost.
func TestConfigCommandsRefuseAnUnnamedOrUnsafeRemote(t *testing.T) {
	// negative control: drop the `u.Scheme == "http" && !privateAddr(...)` guard in apiBase — the
	// public plain-http case stops failing, and a root token plus every credential on a host goes
	// out in the clear.
	env := func(url, token string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			switch k {
			case "PSTACK_API_URL":
				return url, true
			case "PSTACK_TOKEN":
				return token, true
			}
			return "", false
		}
	}
	for _, c := range []struct{ url, token, want string }{
		{"", "tok", "PSTACK_API_URL"},
		{"http://api.example.com", "tok", "plain http"},
		{"http://198.51.100.7:7878", "tok", "plain http"},
		{"ftp://api.example.com", "tok", "http(s) URL"},
		{"not a url at all", "tok", "http(s) URL"},
		{"https://api.example.com", "", "PSTACK_TOKEN"},
	} {
		if _, _, ex := apiBase(env(c.url, c.token)); ex == nil || !strings.Contains(ex.Msg, c.want) {
			t.Errorf("apiBase(%q, %q): want /%s/, got %v", c.url, c.token, c.want, ex)
		}
	}
	// Allowed: TLS anywhere, and plain HTTP only where it cannot leave the machine or the local
	// network — which is what the generated boot step uses to reach the control container.
	for _, u := range []string{"https://api.example.com", "http://127.0.0.1:7878", "http://localhost:7878", "http://172.18.0.4:7878", "https://api.example.com/"} {
		base, tok, ex := apiBase(env(u, "tok"))
		if ex != nil || tok != "tok" || strings.HasSuffix(base, "/") {
			t.Errorf("apiBase(%q) = %q, %v", u, base, ex)
		}
	}
	// Without a terminal there is nowhere to ask, and there is no flag by design.
	if _, ex := configPassphrase(IO{Stdin: strings.NewReader(""), Stderr: &bytes.Buffer{}, Env: noEnv}, false); ex == nil || !strings.Contains(ex.Msg, "PSTACK_CONFIG_KEY") {
		t.Errorf("no passphrase, no tty: %v", ex)
	}
}

// The round trip, end to end against a real HTTP server: export → seal → unseal → apply. It is one
// test rather than three because each scrypt derivation costs about a second by design, and this
// way the bytes that come back out are provably the bytes that went in.
func TestPullAndPushConfigRoundTrip(t *testing.T) {
	// negative control: write `body` instead of `sealed` in pullConfig — the file then contains the
	// document in the clear and the "not sealed" check fails.
	// negative control: POST `sealed` instead of `plain` — the API receives the envelope, not the
	// document, and the `posted != doc` check fails.
	// negative control: replace `if isTerminal(errOut)` with `if true` — the notifier URL, which is
	// itself the credential, lands in what at boot is /var/log/cloud-init-output.log.
	// negative control: print `string(body)` instead of decoding the apply summary — the `trusts` the
	// route echoes back land on stdout, and the stdout check fails.
	// negative control: make cloudInit tolerate a failed Unseal — a wrong passphrase stops being
	// caught while a human is present, and the last check fails.
	// negative control: join `also` with ", " instead of andList — the metadata warning changes and
	// the "It also carries" check fails.
	const pass = "correct horse battery staple"
	const secretURL = "https://hooks.slack.example/T0/B0/XXXTHISISTHESECRET"
	doc := `{"version":1,"pstackVersion":"9.9.9","exportedAt":1,"skipped":[],"users":[],"tokens":[],"vars":[],` +
		`"notifiers":[{"type":"slack","name":"ops","config":{"webhookUrl":"` + secretURL + `"},"events":["job.failed"],"enabled":true,"secret":"sig","createdAt":1}],` +
		`"sso":null,"registries":[{"registry":"ghcr.io","username":"bot","password":"pw"}],"routing":[],"specs":[]}`

	var mu sync.Mutex
	var method, path, auth, posted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		if r.Method == http.MethodGet {
			fmt.Fprint(w, doc)
			return
		}
		b, _ := stdio.ReadAll(r.Body)
		posted = string(b)
		// The real route answers with `trusts` too, and every string in it is a credential. This is
		// here so the CLI is exercised against the shape it will actually meet.
		fmt.Fprint(w, `{"trusts":["send slack notifications to webhookUrl=`+secretURL+`"],"created":["user alice"],"skipped":["notifier ops: already registered"]}`)
	}))
	defer srv.Close()

	env := func(k string) (string, bool) {
		switch k {
		case "PSTACK_API_URL":
			return srv.URL, true
		case "PSTACK_TOKEN":
			return "root-token", true
		case "PSTACK_CONFIG_KEY":
			return pass, true
		}
		return "", false
	}
	file := filepath.Join(t.TempDir(), "export.sealed")
	var out, errOut bytes.Buffer
	streams := func() IO {
		out.Reset()
		errOut.Reset()
		return IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut, Env: env}
	}

	p, e := ParseArgs([]string{"pull", "config", "-o", file}, env)
	if e != nil {
		t.Fatal(e)
	}
	if ex := pullConfig(p, streams()); ex != nil {
		t.Fatalf("pull: %+v", ex)
	}
	if method != http.MethodGet || path != "/api/config" || auth != "Bearer root-token" {
		t.Errorf("GET %s %s auth=%q", method, path, auth)
	}
	sealed, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// SEALED, and 0600. The client seals so the passphrase never leaves this process; the mode is
	// set at creation so the window where a world-readable file holds the export does not exist.
	var envelope struct {
		Sealed string `json:"sealed"`
	}
	if json.Unmarshal(sealed, &envelope) != nil || envelope.Sealed != "scrypt-aes256gcm" {
		t.Errorf("not a sealed envelope: %.120s", sealed)
	}
	if bytes.Contains(sealed, []byte("webhookUrl")) || bytes.Contains(sealed, []byte(secretURL)) {
		t.Error("the export was written in the clear")
	}
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v (%v)", st.Mode().Perm(), err)
	}
	for what, s := range map[string]string{"stdout": out.String(), "stderr": errOut.String(), "the file": string(sealed)} {
		if strings.Contains(s, pass) {
			t.Errorf("the passphrase appears in %s", what)
		}
	}

	// Push it back. -y with a non-terminal stderr is the boot case: the pre-write summary must
	// become a COUNT, because the notifier URL in it is itself a credential and stderr there is
	// /var/log/cloud-init-output.log.
	p, e = ParseArgs([]string{"push", "config", "-i", file, "-y"}, env)
	if e != nil {
		t.Fatal(e)
	}
	if ex := pushConfig(p, streams()); ex != nil {
		t.Fatalf("push: %+v", ex)
	}
	if method != http.MethodPost || path != "/api/config" || auth != "Bearer root-token" {
		t.Errorf("POST %s %s auth=%q", method, path, auth)
	}
	if posted != doc {
		t.Errorf("the API did not receive the document that was exported:\n%s", posted)
	}
	if !strings.Contains(out.String(), "created  user alice") || !strings.Contains(out.String(), "skipped  notifier ops: already registered") {
		t.Errorf("apply summary:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "trust 2 registries and notifier URLs") {
		t.Errorf("no pre-write count:\n%s", errOut.String())
	}
	if strings.Contains(errOut.String(), secretURL) || strings.Contains(errOut.String(), "ghcr.io") {
		t.Errorf("the trust list was written to a pipe:\n%s", errOut.String())
	}
	// The route echoes `trusts` back in its 200. Decoding it and printing only created/skipped is
	// what keeps those credentials off stdout as well.
	if strings.Contains(out.String(), secretURL) {
		t.Errorf("the API's echoed trust list was printed:\n%s", out.String())
	}
	// Unattended and NOT told so: refused rather than applied.
	p, _ = ParseArgs([]string{"push", "config", "-i", file}, env)
	if ex := pushConfig(p, streams()); ex == nil || !strings.Contains(ex.Msg, "-y") {
		t.Errorf("push without -y and without a terminal: %v", ex)
	}

	// `cloud-init --config` embeds THAT file, and proves the key opens it while a human is still
	// present — the host has nobody to ask, so a wrong passphrase there is a boot that comes up with
	// none of its credentials.
	p, _ = ParseArgs([]string{"cloud-init", "--domain", "preview.example.com", "--acme-email", "ops@example.com",
		"--password", "dashpw", "-y", "--config", file}, env)
	if ex := cloudInit(p, streams()); ex != nil {
		t.Fatalf("cloud-init --config: %+v", ex)
	}
	if !strings.Contains(out.String(), base64.StdEncoding.EncodeToString(sealed)) {
		t.Error("the rendered cloud-config does not carry the sealed file")
	}
	if strings.Contains(out.String(), secretURL) {
		t.Error("the cloud-config carries the export unsealed")
	}
	// The metadata warning has to name BOTH, because which of the two is in the file is the whole
	// choice the operator is making. Two items is also the wording `andList` had to preserve.
	if !strings.Contains(errOut.String(), "It also carries PSTACK_CONFIG_KEY and the sealed config export itself, which that key opens.") {
		t.Errorf("the warning does not name what the file carries:\n%s", errOut.String())
	}
	wrong := func(k string) (string, bool) {
		if k == "PSTACK_CONFIG_KEY" {
			return "not the passphrase", true
		}
		return env(k)
	}
	p, _ = ParseArgs([]string{"cloud-init", "--domain", "preview.example.com", "--acme-email", "ops@example.com",
		"--password", "dashpw", "-y", "--config", file}, wrong)
	io := streams()
	io.Env = wrong
	if ex := cloudInit(p, io); ex == nil || !strings.Contains(ex.Msg, "wrong passphrase") {
		t.Errorf("a passphrase that does not open the file: %v", ex)
	}
}

func TestAskSecretRefusesRatherThanEchoingIt(t *testing.T) {
	// /dev/null is a descriptor that is provably not a terminal, which is the same state as a
	// terminal whose `stty` is missing: echo cannot be turned off. The instruction is that the
	// passphrase is NEVER echoed, so the only correct behaviour is to read nothing and say why. The
	// descriptor is passed in rather than taken from the process precisely so this is deterministic —
	// `go test` gives the binary a real tty in some terminals and /dev/null in others.
	// negative control: make noEcho return `func() {}, true` when it cannot turn echo off (the shape
	// it had first) — the prompt is printed, the line is read back, and both checks below fail.
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	var out bytes.Buffer
	v, err := askSecret(bufio.NewReader(strings.NewReader("hunter2\n")), devnull, &out, "Config passphrase")
	if err == nil || v != "" {
		t.Errorf("read %q with echo on (%v)", v, err)
	}
	if out.Len() != 0 {
		t.Errorf("prompted for something it could not hide: %q", out.String())
	}
	if err != nil && !strings.Contains(err.Error(), "PSTACK_CONFIG_KEY") {
		t.Errorf("no way out offered: %v", err)
	}
}
