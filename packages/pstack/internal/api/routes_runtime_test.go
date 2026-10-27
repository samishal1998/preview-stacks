package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/share"
)

// web and api log to Loki; db has its own driver, and worker's profile is not selected.
const declaredCompose = `services:
  web: {image: nginx}
  db:
    image: postgres
    logging: {driver: json-file}
  worker: {image: w, profiles: [jobs]}
  api: {image: a, profiles: [web]}
`

func runtimeBody(t *testing.T, s *Server, bearer string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/deployments/pr-1/runtime", nil)
	req.Header.Set("authorization", "Bearer "+bearer)
	s.handle(rec, req)
	var out map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("GET runtime = %d: %s", rec.Code, rec.Body.String())
	}
	return out
}

func TestRuntimeDeclaredServicesOfAStoppedStack(t *testing.T) {
	// The fake docker answers every command empty: nothing runs.
	s := signalsServer(t)
	s.host = exec.NewFake(nil, "")
	crDeploy(t, s, "pr-1", "myapp-pr-1", declaredCompose)

	// negative control: drop the declaredServices KV from deploymentRuntime — the key is absent.
	// negative control: pass nil profiles to LoggedServices — api drops out.
	got := runtimeBody(t, s, "t0ken")
	if !reflect.DeepEqual(got["declaredServices"], []any{"web", "api"}) {
		t.Fatalf("declaredServices = %#v", got["declaredServices"])
	}
	if c, _ := got["containers"].([]any); len(c) != 0 {
		t.Fatalf("containers = %v, want none running", c)
	}

	t.Run("a share link does not get them", func(t *testing.T) {
		// negative control: drop the KindShare check — the share body carries the key.
		token, _, err := share.Sign("t0ken", "pr-1", []share.View{share.Details}, 60_000, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := runtimeBody(t, s, token)["declaredServices"]; ok {
			t.Fatalf("share body has declaredServices = %v", v)
		}
	})

	t.Run("an unreadable compose gives none", func(t *testing.T) {
		// negative control: answer nil for an unreadable compose — the JSON is null, not [].
		s := signalsServer(t)
		s.host = exec.NewFake(nil, "")
		dep := crDeploy(t, s, "pr-1", "myapp-pr-1", declaredCompose)
		if err := os.Remove(filepath.Join(dep.Dir, "compose.yml")); err != nil {
			t.Fatal(err)
		}
		if got := runtimeBody(t, s, "t0ken")["declaredServices"]; !reflect.DeepEqual(got, []any{}) {
			t.Fatalf("declaredServices = %#v, want []", got)
		}
	})
}
