package jsonx

import (
	"math"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
)

func TestNumbersAndEscaping(t *testing.T) {
	// negative control: use json.Marshal — the html key is escaped to < and 1e21 stays 1e+21.
	var f struct {
		PerKey map[string]string `json:"perKey"`
	}
	testfacts.Load(t, "json-numbers.json", &f)
	cases := map[string]any{
		"msEpoch": int64(1755000000000),
		"float":   1.5,
		"one":     1.0,
		"big":     1e21,
		"tiny":    1e-7,
		"html":    "<a href=\"x\">&  </a>",
		"unicode": "ü✓🚀",
		"nan":     math.NaN(),
		"inf":     math.Inf(1),
	}
	for k, v := range cases {
		want := f.PerKey[k]
		var got string
		switch x := v.(type) {
		case float64:
			got = Number(x)
		default:
			b, err := Marshal(x)
			if err != nil {
				t.Fatal(err)
			}
			got = string(b)
		}
		if got != want {
			t.Errorf("%s: got %s, bun says %s", k, got, want)
		}
	}
}

func TestPrettyPrintIsTwoSpaces(t *testing.T) {
	// negative control: SetIndent("", "\t") — the byte comparison fails.
	type body struct {
		OK   bool     `json:"ok"`
		List []string `json:"list"`
		Nil  *bool    `json:"busy"`
	}
	b, _ := MarshalIndent(body{OK: true, List: []string{"a"}})
	want := "{\n  \"ok\": true,\n  \"list\": [\n    \"a\"\n  ],\n  \"busy\": null\n}"
	if string(b) != want {
		t.Errorf("got\n%s\nwant\n%s", b, want)
	}
	if b[len(b)-1] == '\n' {
		t.Error("trailing newline must be trimmed — the webhook signature covers the bytes")
	}
}

func TestObjectOrder(t *testing.T) {
	// negative control: marshal Object through a map[string]any — keys come out sorted, not in order.
	b, _ := Marshal(Object{{"z", 1}, {"a", "<&>"}, {"m", nil}})
	if string(b) != `{"z":1,"a":"<&>","m":null}` {
		t.Errorf("got %s", b)
	}
}
