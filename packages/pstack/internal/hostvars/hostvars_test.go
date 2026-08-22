package hostvars

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

func open(t *testing.T) *HostVars {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s)
}

// negative control: drop the `existing == 1 && !secret` refusal → the downgrade succeeds.
func TestPutListAndSecretDowngradeRefused(t *testing.T) {
	h := open(t)
	if created, err := h.Put("REGION", "eu", false); err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	if created, err := h.Put("REGION", "us", false); err != nil || created {
		t.Fatalf("update: %v %v", created, err)
	}
	if _, err := h.Put("DB_PASSWORD", "hunter2", true); err != nil {
		t.Fatal(err)
	}
	rows, err := h.List()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rows)
	if !strings.HasPrefix(string(b), `[{"name":"DB_PASSWORD","value":null,"secret":true,"updatedAt":`) ||
		!strings.Contains(string(b), `{"name":"REGION","value":"us","secret":false,`) {
		t.Fatalf("list %s", b)
	}
	_, err = h.Put("DB_PASSWORD", "x", false)
	if !IsError(err) || !strings.Contains(err.Error(), `"DB_PASSWORD" is a secret. Making it a readable variable would reveal`) {
		t.Fatalf("downgrade: %v", err)
	}
	if _, err := h.Put("REGION", "eu", true); err != nil {
		t.Fatalf("upgrade to secret must be allowed: %v", err)
	}
	vars, secrets, _ := h.ResolveMaps()
	if len(vars) != 0 || secrets["REGION"] != "eu" || secrets["DB_PASSWORD"] != "hunter2" {
		t.Fatalf("maps %v %v", vars, secrets)
	}
	vals, _ := h.SecretValues()
	if len(vals) != 2 {
		t.Fatalf("secret values %v", vals)
	}
	if ok, _ := h.Remove("REGION"); !ok {
		t.Fatal("remove")
	}
	if ok, _ := h.Remove("REGION"); ok {
		t.Fatal("remove twice")
	}
}

// negative control: replace nameRe with `.*` → the bad-name case passes Put.
func TestValidation(t *testing.T) {
	h := open(t)
	_, err := h.Put("1abc", "x", true)
	if !IsError(err) || err.Error() != `"1abc" is not a usable name — letters, digits and _ only, not starting with a digit (it has to be referenceable as ${secrets.NAME} in a spec)` {
		t.Fatalf("name: %v", err)
	}
	_, err = h.Put("ok-no", "x", false)
	if !IsError(err) || !strings.Contains(err.Error(), "${vars.NAME}") {
		t.Fatalf("name ns: %v", err)
	}
	_, err = h.Put("OK", "", false)
	if !IsError(err) || err.Error() != "an empty value would make every spec referencing it fail to resolve — delete the entry instead" {
		t.Fatalf("empty: %v", err)
	}
	rows, _ := h.List()
	if rows == nil || len(rows) != 0 {
		t.Fatalf("empty list must be [] not null: %v", rows)
	}
}
