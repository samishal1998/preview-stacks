package terminal

import (
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

// negative control: return true for KindShare in MayOpenTerminal → the share case fails.
func TestPrincipals(t *testing.T) {
	root := auth.Principal{Kind: auth.KindRoot}
	share := auth.Principal{Kind: auth.KindShare, Deployment: "pr-1"}
	user := auth.Principal{Kind: auth.KindUser, User: &auth.UserRow{Username: "bob", Role: "admin"}}
	viewer := auth.Principal{Kind: auth.KindUser, User: &auth.UserRow{Username: "eve", Role: "viewer"}}
	if ActorOf(root) != "root (PSTACK_TOKEN)" || ActorOf(share) != "share-link (pr-1)" || ActorOf(user) != "bob" {
		t.Fatal("actorOf")
	}
	if !MayOpenTerminal(root) || MayOpenTerminal(share) || !MayOpenTerminal(user) || MayOpenTerminal(viewer) {
		t.Fatal("mayOpenTerminal")
	}
	if !IsShell("bash") || IsShell("cmd") || IsShell(nil) || IsShell(1) {
		t.Fatal("isShell")
	}
	if strings.Join(ExecArgv("abc; rm -rf /", "sh"), "\x00") != "docker\x00exec\x00-i\x00abc; rm -rf /\x00sh" {
		t.Fatal("argv must not be re-split")
	}
}

// negative control: drop `AND ended_at IS NULL` from Close → the second Close changes endedAt.
func TestAudit(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := NewAudit(s)
	id, err := a.Open(OpenArgs{Actor: "bob", Deployment: "pr-1", Container: "web-1", ContainerID: "abc", Shell: "sh"})
	if err != nil || id != 1 {
		t.Fatalf("open %d %v", id, err)
	}
	rows, _ := a.Recent(0)
	b, _ := jsonx.Marshal(rows)
	if !strings.HasPrefix(string(b), `[{"id":1,"actor":"bob","deployment":"pr-1","container":"web-1","shell":"sh","startedAt":`) || !strings.HasSuffix(string(b), `,"endedAt":null}]`) {
		t.Fatalf("recent %s", b)
	}
	if err := a.Close(id); err != nil {
		t.Fatal(err)
	}
	rows, _ = a.Recent(100)
	first := *rows[0].EndedAt
	// the second close must not move the timestamp
	time.Sleep(3 * time.Millisecond)
	_ = a.Close(id)
	rows, _ = a.Recent(100)
	if *rows[0].EndedAt != first {
		t.Fatal("close moved ended_at")
	}
	if empty, _ := NewAudit(s).Recent(1); empty == nil {
		t.Fatal("nil")
	}
}
