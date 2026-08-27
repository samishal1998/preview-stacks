package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oascmd "github.com/samishal1998/openapi-commands"
	"github.com/spf13/cobra"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/apicli"
)

// lockOperations is the checked-in oascmd.lock.json — the record of the CLI surface the generator
// last emitted. Reading it here is what lets these tests talk about the generated tree without
// re-running the generator.
func lockOperations(t *testing.T) map[string]struct {
	Command string `json:"command"`
	Method  string `json:"method"`
	Path    string `json:"path"`
} {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "apicli", "oascmd.lock.json"))
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}
	var lock struct {
		Operations map[string]struct {
			Command string `json:"command"`
			Method  string `json:"method"`
			Path    string `json:"path"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatalf("parse the lock: %v", err)
	}
	if len(lock.Operations) == 0 {
		t.Fatal("the lock lists no operations")
	}
	return lock.Operations
}

// `pstack --help` states how many commands `api` has. A constant, because building the tree needs a
// resolved host — so this is what keeps the constant true.
//
// negative control: change OperationCount to 68 — this fails, and so does the sentence a reader
// trusts before they have a host to try it on.
func TestOperationCountMatchesTheGeneratedTree(t *testing.T) {
	if got, want := apicli.OperationCount, len(lockOperations(t)); got != want {
		t.Fatalf("apicli.OperationCount is %d, the generated tree has %d — update the constant", got, want)
	}
}

// Every group in `pstack api --help` says what it is. A blank column is the default when a tag is
// added to the spec and nothing else, because oascmd derives the group from the tag NAME and does
// not carry its description.
//
// negative control: delete the "jobs" entry from groupShort — this fails, naming it, instead of
// shipping a help listing with a blank line in it.
func TestEveryAPIGroupIsDescribed(t *testing.T) {
	groups := map[string]bool{}
	for _, op := range lockOperations(t) {
		if first, _, ok := strings.Cut(op.Command, " "); ok {
			groups[first] = true
		}
	}
	if len(groups) == 0 {
		t.Fatal("no groups found in the lock")
	}
	for g := range groups {
		if groupShort[g] == "" {
			t.Errorf("group %q has no line in groupShort, so `pstack api --help` shows it blank", g)
		}
	}
	// And the reverse: a line for a group that no longer exists is a description of nothing.
	for g := range groupShort {
		if !groups[g] {
			t.Errorf("groupShort describes %q, which the generated tree does not have", g)
		}
	}
}

// The tree builds, every command is reachable, and nothing panics on a nil transport — the check
// `--help` alone does not make, because cobra prints help without ever constructing a request.
//
// negative control: pass a zero ExecOptions with no BaseURL and drop the guard in apiCmd — the
// commands still build, which is why this test asserts the SHAPE rather than that it did not crash.
func TestTheGeneratedTreeMatchesTheLock(t *testing.T) {
	tree := apicli.NewCommandTree(oascmd.ExecOptions{BaseURL: "https://pstack.invalid"})
	got := map[string]bool{}
	var collect func(prefix string, c *cobra.Command)
	collect = func(prefix string, c *cobra.Command) {
		got[prefix] = true
		for _, sub := range c.Commands() {
			collect(prefix+" "+sub.Name(), sub)
		}
	}
	for _, c := range tree {
		collect(c.Name(), c)
	}
	for _, op := range lockOperations(t) {
		if !got[op.Command] {
			t.Errorf("the lock says `%s` exists (%s %s); the built tree has no such command", op.Command, op.Method, op.Path)
		}
	}
}

// `pstack api --help` must work with no PSTACK_API_URL: it asks what the commands ARE, and the
// answer does not depend on a host.
//
// negative control: drop isHelpOnly and resolve the host unconditionally — `pstack api --help` on a
// laptop then answers with a sentence about PSTACK_API_URL instead of the list.
func TestHelpNeedsNoHost(t *testing.T) {
	for _, argv := range [][]string{{"api"}, {"api", "--help"}, {"api", "deployments", "--help"}, {"api", "help"}} {
		if !isHelpOnly(argv[1:]) {
			t.Errorf("%v should not need a configured host", argv)
		}
	}
	// And a real invocation must still be checked, or the first thing a caller learns is a
	// connection error against a placeholder hostname.
	for _, argv := range [][]string{{"api", "deployments", "list"}, {"api", "jobs", "get", "--job-id", "x"}} {
		if isHelpOnly(argv[1:]) {
			t.Errorf("%v is a real call and must resolve PSTACK_API_URL first", argv)
		}
	}
}

// A missing PSTACK_API_URL names the variable and says why there is no default.
//
// negative control: return a bare "not configured" — the caller has to read the source to learn
// which variable, and the sentence about a guess talking to the wrong host is the whole reason it
// has no default.
func TestUnconfiguredHostRefusesByName(t *testing.T) {
	ex := apiCmd([]string{"api", "deployments", "list"}, IO{Env: func(string) (string, bool) { return "", false }})
	if ex == nil {
		t.Fatal("no host configured, but the call was attempted")
	}
	if !strings.Contains(ex.Msg, "PSTACK_API_URL") {
		t.Fatalf("the refusal does not name PSTACK_API_URL: %q", ex.Msg)
	}
}

// A configured URL with no token names PSTACK_TOKEN and points at the command that mints one —
// NOT at apiBase's `/api/config` sentence, which is about the root-token-only export and would be
// false for `pstack api deployments list`.
//
// negative control: return apiBase's error unchanged — the message tells a developer listing
// deployments that they need the host's root token, which they do not.
func TestMissingTokenPointsAtTheRightCredential(t *testing.T) {
	env := func(k string) (string, bool) {
		if k == "PSTACK_API_URL" {
			return "https://api.example.com", true
		}
		return "", false
	}
	ex := apiCmd([]string{"api", "deployments", "list"}, IO{Env: env})
	if ex == nil {
		t.Fatal("no token, but the call was attempted")
	}
	if !strings.Contains(ex.Msg, "PSTACK_TOKEN") || strings.Contains(ex.Msg, "root-token only") {
		t.Fatalf("wrong refusal for a non-config route: %q", ex.Msg)
	}
}
