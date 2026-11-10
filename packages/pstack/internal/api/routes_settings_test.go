package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
)

// lockProbe is a request body that notes, on every read, whether writeMu was free.
type lockProbe struct {
	s      *Server
	r      *strings.Reader
	locked bool
}

func (p *lockProbe) Read(b []byte) (int, error) {
	if p.s.writeMu.TryLock() {
		p.s.writeMu.Unlock()
	} else {
		p.locked = true
	}
	return p.r.Read(b)
}

// A body that stalls must not hold the server-wide write lock.
// negative control: take s.writeMu before bodyObject(r) in putSetting → the read sees it held.
func TestPutSettingReadsTheBodyOutsideWriteMu(t *testing.T) {
	s := settingsFixture(t, 0)
	probe := &lockProbe{s: s, r: strings.NewReader(`{"value":3}`)}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/settings/max_jobs", probe)
	if err := s.routes(w, r, "/api/settings/max_jobs", &auth.Principal{Kind: auth.KindRoot}, map[string]string{}); err != nil {
		s.fail(w, err)
	}
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if probe.locked {
		t.Fatal("the body was read under writeMu")
	}
}

// Go rule 14: Emit runs its listeners there and then, so no write emits under writeMu.
// negative control: move the Emit into the writeMu closure in putDeployment, or in either spec
// branch of specRoutes → that event's listener finds writeMu held.
func TestWritesEmitOutsideWriteMu(t *testing.T) {
	s, _ := crServer(t)
	seen, held := []string{}, []string{}
	off := s.bus.On(func(e events.Event) {
		switch e.Event {
		case "deployment.created", "deployment.updated", "spec.stored", "spec.deleted":
			seen = append(seen, e.Event)
			if s.writeMu.TryLock() {
				s.writeMu.Unlock()
			} else {
				held = append(held, e.Event)
			}
		}
	})
	t.Cleanup(off)
	crPut(t, s, "pr-9", "myapp-pr-9", crCompose)
	crPut(t, s, "pr-9", "myapp-pr-9", crCompose)
	body, _ := json.Marshal(map[string]string{"spec": crSpec("web")})
	if code, out := crCall(t, s, "PUT", "/api/specs/web", string(body)); code != 201 {
		t.Fatalf("PUT spec: %d %v", code, out)
	}
	if code, out := crCall(t, s, "DELETE", "/api/specs/web", ""); code != 200 {
		t.Fatalf("DELETE spec: %d %v", code, out)
	}
	if want := "deployment.created deployment.updated spec.stored spec.deleted"; strings.Join(seen, " ") != want {
		t.Fatalf("events = %q, want %q", seen, want)
	}
	if len(held) != 0 {
		t.Fatalf("emitted under writeMu: %q", held)
	}
}
