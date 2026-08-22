package omap

import (
	"encoding/json"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
)

func TestJSPropertyOrder(t *testing.T) {
	// negative control: return m.keys as-is from Keys() — the hoisted "0","1" land last.
	var f struct {
		Documents []struct {
			Name string `json:"name"`
			JSON string `json:"json"`
		} `json:"documents"`
	}
	testfacts.Load(t, "yaml.json", &f)
	m := From("z", int64(1), "a", int64(2), "m", int64(3), "1", int64(4), "0", int64(5))
	b, _ := m.MarshalJSON()
	for _, d := range f.Documents {
		if d.Name == "key order is insertion order" {
			if string(b) != d.JSON {
				t.Errorf("got %s, bun says %s", b, d.JSON)
			}
		}
	}
}

func TestSetKeepsPositionDeleteRemoves(t *testing.T) {
	// negative control: append on every Set — "a" moves to the end.
	m := From("a", 1, "b", 2)
	m.Set("a", 9)
	m.Delete("b")
	m.Set("c", 3)
	if got := m.Keys(); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Errorf("keys = %v", got)
	}
}

func TestRoundTripThroughJSON(t *testing.T) {
	// negative control: decode with plain json.Unmarshal into map[string]any — order is lost and ints become floats.
	src := `{"services":{"web":{"image":"nginx","ports":["80:80"],"restart":"no","n":1,"f":1.5}},"x-y":null,"1":true}`
	m := New()
	if err := json.Unmarshal([]byte(src), m); err != nil {
		t.Fatal(err)
	}
	b, _ := m.MarshalJSON()
	if string(b) != `{"1":true,"services":{"web":{"image":"nginx","ports":["80:80"],"restart":"no","n":1,"f":1.5}},"x-y":null}` {
		t.Errorf("got %s", b)
	}
	if v, _ := m.GetMap("services").GetMap("web").Get("n"); v != int64(1) {
		t.Errorf("n should be int64 1, got %T %v", v, v)
	}
	c := m.Clone()
	c.GetMap("services").Delete("web")
	if m.GetMap("services").Len() != 1 {
		t.Error("Clone must be deep")
	}
}
