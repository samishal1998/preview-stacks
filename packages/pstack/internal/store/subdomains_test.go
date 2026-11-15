package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestRouteSubdomains(t *testing.T) {
	s := openRoutes(t)
	mustCreate(t, s.DB, "checkout", "d1")
	if got := mustGet(t, s.DB, "checkout").Subdomains; got != "" {
		t.Fatalf("a new route has subdomains %q, want off", got)
	}
	stamp(t, s, "checkout")
	if err := SetCustomRouteSubdomains(s.DB, "checkout", "one"); err != nil {
		t.Fatal(err)
	}
	r := mustGet(t, s.DB, "checkout")
	if r.Subdomains != "one" || r.DeploymentID != "d1" {
		t.Fatalf("got %+v, want one on d1", r)
	}
	// The render's tie-break is updated_at: a subdomains change is not a new claim.
	if r.UpdatedAt != 1 {
		t.Fatalf("updated_at moved to %d", r.UpdatedAt)
	}
	if err := SetCustomRouteSubdomains(s.DB, "checkout", ""); err != nil || mustGet(t, s.DB, "checkout").Subdomains != "" {
		t.Fatalf("off: %v", err)
	}
	if err := SetCustomRouteSubdomains(s.DB, "nope", "one"); !errors.Is(err, ErrCustomRouteNotFound) {
		t.Fatalf("missing route: %v", err)
	}
	// Survives an unassign and a reassign: it belongs to the route, not the assignment.
	_ = SetCustomRouteSubdomains(s.DB, "checkout", "any")
	if _, err := UnassignCustomRoute(s.DB, "checkout"); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s.DB, "checkout").Subdomains; got != "any" {
		t.Fatalf("after unassign: %q", got)
	}
}

func TestDeploymentSubdomains(t *testing.T) {
	s := openRoutes(t)
	got, err := DeploymentSubdomains(s.DB, "d1")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, c := range [][2]string{{"web", "one"}, {"api", "any"}, {"web", "any"}} {
		if err := SetDeploymentSubdomain(s.DB, "d1", c[0], c[1]); err != nil {
			t.Fatal(err)
		}
	}
	_ = SetDeploymentSubdomain(s.DB, "d2", "web", "one")
	got, _ = DeploymentSubdomains(s.DB, "d1")
	if want := map[string]string{"api": "any", "web": "any"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if err := SetDeploymentSubdomain(s.DB, "d1", "api", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ = DeploymentSubdomains(s.DB, "d1"); !reflect.DeepEqual(got, map[string]string{"web": "any"}) {
		t.Fatalf("after off: %v", got)
	}
	if err := ForgetDeploymentSubdomains(s.DB, "d1"); err != nil {
		t.Fatal(err)
	}
	if got, _ = DeploymentSubdomains(s.DB, "d1"); len(got) != 0 {
		t.Fatalf("after forget: %v", got)
	}
	if got, _ = DeploymentSubdomains(s.DB, "d2"); len(got) != 1 {
		t.Fatalf("forget reached d2: %v", got)
	}
}
