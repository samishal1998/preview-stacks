package js

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/testfacts"
)

// Every table here was MEASURED on Bun (packages/conformance/gen/facts.ts); the Go helpers are
// graded against it rather than against what the language reference says.

type coerce struct {
	Numbers []struct {
		Input     string `json:"input"`
		Number    any    `json:"number"`
		IsFinite  bool   `json:"isFinite"`
		IsInteger bool   `json:"isInteger"`
	} `json:"numbers"`
	Strings []struct {
		Input  string `json:"input"`
		String string `json:"string"`
	} `json:"strings"`
	Queries []struct {
		Raw      string            `json:"raw"`
		Entries  [][]string        `json:"entries"`
		LastWins map[string]string `json:"lastWins"`
	} `json:"queries"`
	Encode []struct {
		Input string `json:"input"`
		Out   string `json:"encodeURIComponent"`
	} `json:"encode"`
	Decode []struct {
		Input   string  `json:"input"`
		Decoded *string `json:"decoded"`
		Throws  bool    `json:"throws"`
	} `json:"decode"`
	Lengths []struct {
		Input    string `json:"input"`
		JSLength int    `json:"jsLength"`
		Slice02  string `json:"slice0_2"`
	} `json:"lengths"`
}

func TestParseNumberMatchesBun(t *testing.T) {
	// negative control: make ParseNumber return strconv.ParseFloat's answer — "0x10", " 12 " and "" fail.
	var f coerce
	testfacts.Load(t, "coerce.json", &f)
	for _, row := range f.Numbers {
		got := ParseNumber(row.Input)
		var want string
		switch n := row.Number.(type) {
		case string:
			want = n
		case float64:
			want = NumberString(n)
		}
		gotS := NumberString(got)
		if math.IsNaN(got) {
			gotS = "NaN"
		}
		if got == 0 && math.Signbit(got) {
			gotS = "-0" // the fixture spells negative zero out; String(-0) is "0" in JS
		}
		if gotS != want {
			t.Errorf("Number(%q) = %s, bun says %s", row.Input, gotS, want)
		}
		if IsFinite(got) != row.IsFinite || IsInteger(got) != row.IsInteger {
			t.Errorf("Number(%q): isFinite/isInteger = %v/%v, bun says %v/%v", row.Input, IsFinite(got), IsInteger(got), row.IsFinite, row.IsInteger)
		}
	}
}

func TestNumberStringMatchesBun(t *testing.T) {
	// negative control: use strconv.FormatFloat(f,'g',-1,64) — 1e21 and 0.30000000000000004 differ.
	var f coerce
	testfacts.Load(t, "coerce.json", &f)
	for _, row := range f.Strings {
		var n float64
		if err := json.Unmarshal([]byte(row.Input), &n); err != nil {
			t.Fatal(err)
		}
		if got := NumberString(n); got != row.String {
			t.Errorf("String(%s) = %q, bun says %q", row.Input, got, row.String)
		}
	}
}

func TestParseQueryMatchesURLSearchParams(t *testing.T) {
	// negative control: use net/url.ParseQuery — "a=1;b=2" and "a=%zz" differ.
	var f coerce
	testfacts.Load(t, "coerce.json", &f)
	for _, row := range f.Queries {
		got := ParseQuery(row.Raw)
		if len(got) != len(row.Entries) {
			t.Errorf("ParseQuery(%q) = %v, bun says %v", row.Raw, got, row.Entries)
			continue
		}
		for i, e := range row.Entries {
			if got[i].K != e[0] || got[i].V != e[1] {
				t.Errorf("ParseQuery(%q)[%d] = %v, bun says %v", row.Raw, i, got[i], e)
			}
		}
		_, last := LastWins(got)
		for k, v := range row.LastWins {
			if last[k] != v {
				t.Errorf("LastWins(%q)[%s] = %q, bun says %q", row.Raw, k, last[k], v)
			}
		}
	}
}

func TestEncodeDecodeURIComponentMatchesBun(t *testing.T) {
	// negative control: use url.QueryEscape — "a b" (→ a+b) and "!'()*" differ.
	var f coerce
	testfacts.Load(t, "coerce.json", &f)
	for _, row := range f.Encode {
		if got := EncodeURIComponent(row.Input); got != row.Out {
			t.Errorf("encodeURIComponent(%q) = %q, bun says %q", row.Input, got, row.Out)
		}
	}
	for _, row := range f.Decode {
		got, ok := DecodeURIComponent(row.Input)
		if ok == row.Throws {
			t.Errorf("decodeURIComponent(%q): threw=%v, bun says %v", row.Input, !ok, row.Throws)
		}
		if ok && row.Decoded != nil && got != *row.Decoded {
			t.Errorf("decodeURIComponent(%q) = %q, bun says %q", row.Input, got, *row.Decoded)
		}
	}
}

func TestLenIsUTF16(t *testing.T) {
	// negative control: return len(s) — "🚀" reports 4, bun says 2.
	var f coerce
	testfacts.Load(t, "coerce.json", &f)
	for _, row := range f.Lengths {
		if got := Len(row.Input); got != row.JSLength {
			t.Errorf("Len(%q) = %d, bun says %d", row.Input, got, row.JSLength)
		}
		if got := Slice(row.Input, 0, 2); got != row.Slice02 {
			t.Errorf("Slice(%q,0,2) = %q, bun says %q", row.Input, got, row.Slice02)
		}
	}
}

func TestEscAndShq(t *testing.T) {
	// negative control: swap Esc for html.EscapeString — the double quote becomes &#34;.
	var f struct {
		Esc []struct{ Input, Output string } `json:"esc"`
	}
	testfacts.Load(t, "esc.json", &f)
	for _, row := range f.Esc {
		if got := Esc(row.Input); got != row.Output {
			t.Errorf("esc(%q) = %q, bun says %q", row.Input, got, row.Output)
		}
	}
}

func TestB64URLIsLenient(t *testing.T) {
	// negative control: use base64.RawURLEncoding.DecodeString directly — the padded and the +/ forms fail.
	for _, in := range []string{"aGVsbG8", "aGVsbG8=", "aGVs bG8=", "a+Gv/w==", "a-Gv_w"} {
		if _, err := B64URLDecode(in); err != nil {
			t.Errorf("B64URLDecode(%q) rejected: %v", in, err)
		}
	}
	if B64URL([]byte("hello")) != "aGVsbG8" {
		t.Error("B64URL must be unpadded")
	}
}
