package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/log"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/stack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

type nopSink struct{}

func (nopSink) Emit(log.Level, string) {}

// fakeDockerPath is a PATH whose `docker` answers every command with exit 0 and no output.
func fakeDockerPath(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin + ":" + os.Getenv("PATH")
}

func TestCustomRoutesReconcileAtBoot(t *testing.T) {
	data, dyn := t.TempDir(), t.TempDir()
	logs := []string{}
	opts := Options{DataDir: data, Token: "t0ken", RoutingDir: dyn, Bus: events.New(),
		Env: map[string]string{"PATH": fakeDockerPath(t)}, Log: func(l string) { logs = append(logs, l) }}
	first, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	crDeploy(t, first, "pr-1", "myapp-pr-1", crCompose)
	// Written to the table only, as a crash between commit and render would leave it.
	if err := store.CreateCustomRoute(first.store.DB, "checkout", "pr-1"); err != nil {
		t.Fatal(err)
	}
	first.Stop()
	if _, err := os.Stat(filepath.Join(dyn, routing.CustomRoutesYAML)); !os.IsNotExist(err) {
		t.Fatalf("the file exists before boot: %v", err)
	}

	// negative control: drop s.reconcileCustomRoutes() from New → the file stays missing and this fails.
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if file := crFile(t, s); !strings.Contains(file, "pstack-route-checkout-web:") {
		t.Fatalf("boot did not heal the file:\n%s", file)
	}

	t.Run("a write failure is logged, not fatal", func(t *testing.T) {
		// negative control: have reconcileCustomRoutes call renderCustomRoutes and ignore its error →
		// nothing is logged and this fails.
		notDir := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(notDir, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		bad := opts
		bad.RoutingDir = notDir
		logs = logs[:0]
		again, err := New(bad)
		if err != nil {
			t.Fatalf("boot must survive an unwritable routing dir: %v", err)
		}
		again.Stop()
		if !slices.ContainsFunc(logs, func(l string) bool { return strings.Contains(l, "could not write "+routing.CustomRoutesYAML) }) {
			t.Fatalf("logs = %v", logs)
		}
	})
}

func TestCustomRoutesFollowADeploy(t *testing.T) {
	s, f := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	// The redeploy adds a service.
	crDeploy(t, s, "pr-1", "myapp-pr-1", crCompose+"  c:\n    image: nginx\n    labels: {pstack.routing.port: \"80\"}\n")
	if strings.Contains(crFile(t, s), "pstack-route-checkout-c:") {
		t.Fatal("rendered before the deploy")
	}

	t.Run("an up of a deployment with routes re-renders", func(t *testing.T) {
		// negative control: drop the `s.rerenderRoutesOf(id)` call from startLifecycle → the new
		// service never reaches the file and this fails.
		s.env["PATH"] = fakeDockerPath(t)
		dep, err := s.registry.Get("pr-1")
		if err != nil || dep == nil {
			t.Fatal(dep, err)
		}
		st, err := s.resolveDep("pr-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		job, ok := s.startLifecycle("pr-1", dep, st, jobs.Up, lifecycleOptions{By: "test"})
		if !ok {
			t.Fatal("up refused")
		}
		for deadline := time.Now().Add(10 * time.Second); ; {
			j, _ := s.jobs.Get(job.ID)
			if j.State != jobs.Queued && j.State != jobs.Running {
				if j.State != jobs.OK {
					t.Fatalf("up ended %s: %+v", j.State, j.Outcome)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("up never finished")
			}
			time.Sleep(10 * time.Millisecond)
		}
		s.readiness.Cancel(st.Stack)
		if file := crFile(t, s); !strings.Contains(file, "pstack-route-checkout-c:") {
			t.Fatalf("file after the deploy:\n%s", file)
		}
	})

	t.Run("a deployment with no routes renders nothing", func(t *testing.T) {
		// negative control: drop the DeploymentID == id filter in rerenderRoutesOf → it renders, asks
		// docker for the challenge, and the fake's log grows.
		before := len(f.Log)
		s.rerenderRoutesOf("pr-2")
		if len(f.Log) != before {
			t.Fatalf("docker was asked: %v", f.Log[before:])
		}
	})
}

func TestCustomRoutesForgetUnassigns(t *testing.T) {
	s, _ := crServer(t)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"keep","deployment":"pr-2"}`, 201)
	seen := crEvents(t, s)

	t.Run("DELETE", func(t *testing.T) {
		// negative control: drop the UnassignDeploymentRoutes Tx from removeDeployment → checkout stays
		// on the gone pr-1 and this fails.
		crExpect(t, s, "DELETE", "/api/deployments/pr-1", "", 200)
		routes := crList(t, s)
		if routes[0]["name"] != "checkout" || routes[0]["deployment"] != nil || routes[1]["deployment"] != "pr-2" {
			t.Fatalf("routes = %v", routes)
		}
		file := crFile(t, s)
		if strings.Contains(file, "pstack-route-checkout-web:") || !strings.Contains(file, "pstack-route-keep-web:") {
			t.Fatalf("file:\n%s", file)
		}
		// negative control: drop the route.unassigned Emit loop → this fails.
		want := `route.unassigned {"route":"checkout","deployment":"pr-1","stack":"myapp-pr-1","reason":"forgotten"}`
		if !slices.Equal(*seen, []string{want}) {
			t.Fatalf("events = %v", *seen)
		}
	})

	t.Run("down with forget", func(t *testing.T) {
		// negative control: as above, drop the unassign → keep stays assigned and the file stays.
		*seen = (*seen)[:0]
		dep, err := s.registry.Get("pr-2")
		if err != nil || dep == nil {
			t.Fatal(dep, err)
		}
		st, err := s.resolveDep("pr-2", nil)
		if err != nil {
			t.Fatal(err)
		}
		s.forgetAfterDown(dep, st, stack.Outcome{OK: true}, nopSink{})
		if got, _ := s.registry.Get("pr-2"); got != nil {
			t.Fatal("not forgotten")
		}
		if routes := crList(t, s); routes[1]["name"] != "keep" || routes[1]["deployment"] != nil {
			t.Fatalf("routes = %v", routes)
		}
		if file := crFile(t, s); file != "" {
			t.Fatalf("the last route went, the file must too:\n%s", file)
		}
		want := `route.unassigned {"route":"keep","deployment":"pr-2","stack":"myapp-pr-2","reason":"forgotten"}`
		if !slices.Equal(*seen, []string{want}) {
			t.Fatalf("events = %v", *seen)
		}
	})
}

func TestCustomRoutesWakeTheirDeployment(t *testing.T) {
	s, _ := crServer(t)
	wild := "version: 1\nstack: myapp-pr-1\nenv:\n  PREVIEW_DOMAIN: preview.example.com\n" +
		"compose:\n  file: compose.yml\n  profiles: [web]\n  subdomains: [web]\naxes: []\n"
	compose := crCompose
	if _, err := s.registry.Put("pr-1", wild, registry.PutOptions{ComposeYaml: &compose}); err != nil {
		t.Fatal(err)
	}
	crExpect(t, s, "POST", "/api/custom-routes", `{"name":"checkout","deployment":"pr-1"}`, 201)
	alias, under := "web-checkout.preview.example.com", "shop.web-checkout.preview.example.com"
	sleep := func(id string) {
		t.Helper()
		if err := s.registry.SetSleep(id, &registry.SleepRecord{Since: 1, Reason: "test", Hosts: []string{"web-myapp-" + id + ".preview.example.com"}, Rules: []string{}}); err != nil {
			t.Fatal(err)
		}
		s.reindex()
	}

	t.Run("asleep, the alias wakes it", func(t *testing.T) {
		// negative control: drop the routeWake loop from reindex → Find(alias) is "" and this fails.
		sleep("pr-1")
		if s.sleepIndex.Find(alias) != "pr-1" {
			t.Fatal("the alias host is not in the sleep index")
		}
		// negative control: drop the AliasWildcardRules loop from appendWake → this fails.
		if s.sleepIndex.Find(under) != "pr-1" {
			t.Fatal("the alias wildcard is not in the sleep index")
		}
		s.env["PATH"] = fakeDockerPath(t) // the wake's `up` runs against a docker that answers ok
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = alias
		s.handle(rec, req)
		if rec.Code != 503 || rec.Header().Get("x-pstack-wake") != "1" || !strings.Contains(rec.Body.String(), "Waking your preview") {
			t.Fatalf("%d %v\n%s", rec.Code, rec.Header(), rec.Body.String())
		}
		var wake jobs.Job
		for deadline := time.Now().Add(10 * time.Second); ; {
			i := slices.IndexFunc(s.jobs.List(), func(j jobs.Job) bool { return j.Stack == "myapp-pr-1" && j.Action == jobs.Wake })
			if i >= 0 {
				if wake = s.jobs.List()[i]; wake.State != jobs.Queued && wake.State != jobs.Running {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("no wake finished")
			}
			time.Sleep(10 * time.Millisecond)
		}
		s.readiness.Cancel("myapp-pr-1")
		if wake.State != jobs.OK {
			t.Fatalf("wake ended %s: %+v", wake.State, wake.Outcome)
		}
		// Woken: the record is gone, and the alias keeps the waking page until readiness settles.
		// negative control: apply routeWake to the sleeping entries only (before the waking ones are
		// appended) → this fails.
		if _, ok := s.wakingOf("pr-1"); !ok || s.sleepIndex.Find(alias) != "pr-1" {
			t.Fatalf("waking=%v Find(alias)=%q", ok, s.sleepIndex.Find(alias))
		}
		s.forgetWaking("pr-1")
	})

	t.Run("a move while asleep follows the assignment", func(t *testing.T) {
		// negative control: fill routeWake only at boot (not in every renderCustomRoutes) → the alias
		// still wakes pr-1 and this fails.
		sleep("pr-1")
		sleep("pr-2")
		crExpect(t, s, "PUT", "/api/custom-routes/checkout/assignment", `{"deployment":"pr-2","force":true}`, 200)
		if got := s.sleepIndex.Find(alias); got != "pr-2" {
			t.Fatalf("Find(alias) = %q, want pr-2", got)
		}
	})
}
