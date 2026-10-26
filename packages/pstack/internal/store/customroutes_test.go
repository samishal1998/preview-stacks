package store

import (
	"errors"
	"reflect"
	"testing"
)

func openRoutes(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustCreate(t *testing.T, q Querier, name, dep string) {
	t.Helper()
	if err := CreateCustomRoute(q, name, dep); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

func mustGet(t *testing.T, q Querier, name string) CustomRoute {
	t.Helper()
	r, ok, err := GetCustomRoute(q, name)
	if err != nil || !ok {
		t.Fatalf("get %s: %v %v", name, ok, err)
	}
	return r
}

// stamp pins updated_at so a test can tell "untouched" from "rewritten" at millisecond resolution.
func stamp(t *testing.T, s *Store, name string) {
	t.Helper()
	if _, err := s.DB.Exec("UPDATE custom_routes SET updated_at = 1 WHERE name = ?", name); err != nil {
		t.Fatal(err)
	}
}

func TestCustomRoutes(t *testing.T) {
	t.Run("create, get and list ordered by name; unassigned reads as empty", func(t *testing.T) {
		// negative control: drop ORDER BY name from ListCustomRoutes → rows come back in insert order and this fails
		s := openRoutes(t)
		mustCreate(t, s.DB, "web", "checkout")
		mustCreate(t, s.DB, "api", "")
		mustCreate(t, s.DB, "mid", "billing")
		got, err := ListCustomRoutes(s.DB)
		if err != nil {
			t.Fatal(err)
		}
		var names, deps []string
		for _, r := range got {
			names = append(names, r.Name)
			deps = append(deps, r.DeploymentID)
			if r.CreatedAt == 0 || r.UpdatedAt != r.CreatedAt {
				t.Fatalf("%s timestamps %d/%d", r.Name, r.CreatedAt, r.UpdatedAt)
			}
		}
		if !reflect.DeepEqual(names, []string{"api", "mid", "web"}) || !reflect.DeepEqual(deps, []string{"", "billing", "checkout"}) {
			t.Fatalf("list %v %v", names, deps)
		}
		var null int
		s.DB.QueryRow("SELECT COUNT(*) FROM custom_routes WHERE deployment_id IS NULL").Scan(&null)
		if null != 1 {
			t.Fatalf("unassigned must be stored as NULL, %d NULL rows", null)
		}
		if _, ok, err := GetCustomRoute(s.DB, "nope"); ok || err != nil {
			t.Fatalf("get unknown: %v %v", ok, err)
		}
	})

	t.Run("an empty table lists as an empty, non-nil slice", func(t *testing.T) {
		// negative control: `var out []CustomRoute` in ListCustomRoutes → a nil slice (JSON null) comes back and this fails
		got, err := ListCustomRoutes(openRoutes(t).DB)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("%v %v", got, err)
		}
	})

	t.Run("a duplicate name is ErrCustomRouteExists and leaves the row alone", func(t *testing.T) {
		// negative control: drop ON CONFLICT DO NOTHING → the error is a raw UNIQUE failure, not ErrCustomRouteExists
		s := openRoutes(t)
		mustCreate(t, s.DB, "web", "checkout")
		if err := CreateCustomRoute(s.DB, "web", "billing"); !errors.Is(err, ErrCustomRouteExists) {
			t.Fatalf("duplicate: %v", err)
		}
		if r := mustGet(t, s.DB, "web"); r.DeploymentID != "checkout" {
			t.Fatalf("duplicate changed the row: %+v", r)
		}
	})

	t.Run("assign: unassigned takes it, same deployment is a no-op, another is held", func(t *testing.T) {
		// negative control: drop the `cur.DeploymentID == deploymentID` early return → re-assigning to the holder is refused as held and this fails
		s := openRoutes(t)
		mustCreate(t, s.DB, "web", "")
		prev, err := AssignCustomRoute(s.DB, "web", "checkout", false)
		if err != nil || prev != "" || mustGet(t, s.DB, "web").DeploymentID != "checkout" {
			t.Fatalf("assign unassigned: %q %v", prev, err)
		}
		stamp(t, s, "web")
		prev, err = AssignCustomRoute(s.DB, "web", "checkout", false)
		if err != nil || prev != "checkout" || mustGet(t, s.DB, "web").UpdatedAt != 1 {
			t.Fatalf("same deployment must be a no-op: %q %v %+v", prev, err, mustGet(t, s.DB, "web"))
		}
		prev, err = AssignCustomRoute(s.DB, "web", "billing", false)
		var held ErrCustomRouteHeld
		if !errors.As(err, &held) || held.DeploymentID != "checkout" || prev != "" {
			t.Fatalf("held: %q %v", prev, err)
		}
		if r := mustGet(t, s.DB, "web"); r.DeploymentID != "checkout" || r.UpdatedAt != 1 {
			t.Fatalf("a refused assign changed the row: %+v", r)
		}
	})

	t.Run("assign with force moves in one transaction and reports the previous holder", func(t *testing.T) {
		// negative control: ignore force (always refuse when held) → ErrCustomRouteHeld comes back and this fails
		s := openRoutes(t)
		mustCreate(t, s.DB, "web", "checkout")
		stamp(t, s, "web")
		var prev string
		err := s.Tx(func(q Querier) (err error) {
			prev, err = AssignCustomRoute(q, "web", "billing", true)
			return err
		})
		if err != nil || prev != "checkout" {
			t.Fatalf("force: %q %v", prev, err)
		}
		if r := mustGet(t, s.DB, "web"); r.DeploymentID != "billing" || r.UpdatedAt <= 1 {
			t.Fatalf("after force: %+v", r)
		}
	})

	t.Run("assign, unassign and delete of an unknown name are ErrCustomRouteNotFound", func(t *testing.T) {
		// negative control: return "", nil from DeleteCustomRoute when no row matched → the delete check fails
		s := openRoutes(t)
		if _, err := AssignCustomRoute(s.DB, "nope", "checkout", true); !errors.Is(err, ErrCustomRouteNotFound) {
			t.Fatalf("assign: %v", err)
		}
		if _, err := UnassignCustomRoute(s.DB, "nope"); !errors.Is(err, ErrCustomRouteNotFound) {
			t.Fatalf("unassign: %v", err)
		}
		if _, err := DeleteCustomRoute(s.DB, "nope"); !errors.Is(err, ErrCustomRouteNotFound) {
			t.Fatalf("delete: %v", err)
		}
	})

	t.Run("unassign clears the holder and returns it; twice returns empty", func(t *testing.T) {
		// negative control: SET deployment_id = '' instead of NULL → the NULL count check fails
		s := openRoutes(t)
		mustCreate(t, s.DB, "web", "checkout")
		prev, err := UnassignCustomRoute(s.DB, "web")
		if err != nil || prev != "checkout" || mustGet(t, s.DB, "web").DeploymentID != "" {
			t.Fatalf("unassign: %q %v", prev, err)
		}
		var null int
		s.DB.QueryRow("SELECT COUNT(*) FROM custom_routes WHERE deployment_id IS NULL").Scan(&null)
		if null != 1 {
			t.Fatal("unassigned must be stored as NULL")
		}
		if prev, err = UnassignCustomRoute(s.DB, "web"); err != nil || prev != "" {
			t.Fatalf("unassign twice: %q %v", prev, err)
		}
	})

	t.Run("delete removes the row and returns the holder", func(t *testing.T) {
		// negative control: return the deleted row's name instead of its deployment → prev mismatches and this fails
		s := openRoutes(t)
		mustCreate(t, s.DB, "web", "checkout")
		mustCreate(t, s.DB, "api", "")
		if prev, err := DeleteCustomRoute(s.DB, "web"); err != nil || prev != "checkout" {
			t.Fatalf("delete: %q %v", prev, err)
		}
		if prev, err := DeleteCustomRoute(s.DB, "api"); err != nil || prev != "" {
			t.Fatalf("delete unassigned: %q %v", prev, err)
		}
		if got, _ := ListCustomRoutes(s.DB); len(got) != 0 {
			t.Fatalf("rows left: %+v", got)
		}
	})

	t.Run("UnassignDeploymentRoutes frees only that deployment's routes, sorted", func(t *testing.T) {
		// negative control: drop the WHERE deployment_id = ? → billing's route is freed too and this fails
		s := openRoutes(t)
		mustCreate(t, s.DB, "web", "checkout")
		mustCreate(t, s.DB, "admin", "checkout")
		mustCreate(t, s.DB, "pay", "billing")
		mustCreate(t, s.DB, "free", "")
		got, err := UnassignDeploymentRoutes(s.DB, "checkout")
		if err != nil || !reflect.DeepEqual(got, []string{"admin", "web"}) {
			t.Fatalf("unassigned %v %v", got, err)
		}
		if r := mustGet(t, s.DB, "pay"); r.DeploymentID != "billing" {
			t.Fatalf("another deployment's route moved: %+v", r)
		}
		for _, n := range []string{"web", "admin"} {
			if r := mustGet(t, s.DB, n); r.DeploymentID != "" {
				t.Fatalf("%s still held: %+v", n, r)
			}
		}
		if got, err := UnassignDeploymentRoutes(s.DB, "checkout"); err != nil || len(got) != 0 {
			t.Fatalf("second call: %v %v", got, err)
		}
	})
}
