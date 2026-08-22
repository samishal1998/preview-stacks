package share

import (
	"reflect"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
)

const token = "test-token-0123456789abcdef"

// Port of test/features.test.ts 'share.ts'.
func TestShare(t *testing.T) {
	t.Run("sign → verify round-trips; anything off is null", func(t *testing.T) {
		// negative control: drop the `exp*1000 <= now` check → the expired verify returns claims
		tok, claims, err := Sign(token, "pr-1", []View{"logs", "logs", "details"}, 60_000, 1_000_000)
		if err != nil {
			t.Fatal(err)
		}
		if !LooksLikeToken(tok) {
			t.Fatalf("shape: %s", tok)
		}
		want := &Claims{Sub: "share", Dep: "pr-1", Views: []View{"logs", "details"}, Iat: 1000, Exp: 1060}
		if !reflect.DeepEqual(claims, want) {
			t.Fatalf("claims %+v", claims)
		}
		if got := Verify(token, tok, 1_050_000); !reflect.DeepEqual(got, want) {
			t.Fatalf("verify %+v", got)
		}
		if Verify(token, tok, 1_060_000) != nil {
			t.Fatal("expired token verified")
		}
		if Verify("other-key", tok, 1_050_000) != nil {
			t.Fatal("wrong key verified")
		}
		parts := strings.Split(tok, ".")
		h, b, sig := parts[0], parts[1], parts[2]
		if Verify(token, h+"."+b+"."+sig[:len(sig)-1]+"x", 1_050_000) != nil {
			t.Fatal("tampered signature verified")
		}
		// A re-encoded body with a different deployment does not carry the old signature.
		forged := js.B64URL(jsonx.Must(&Claims{Sub: "share", Dep: "pr-2", Views: want.Views, Iat: 1000, Exp: 1060}))
		if Verify(token, h+"."+forged+"."+sig, 1_050_000) != nil {
			t.Fatal("forged body verified")
		}
		if Verify(token, token, 1_050_000) != nil {
			t.Fatal("the bearer itself is not a share token")
		}
		if _, _, err := Sign(token, "x", []View{}, 1, 1_000_000); err == nil || !strings.Contains(err.Error(), "views") {
			t.Fatalf("empty views: %v", err)
		}
		if _, _, err := Sign("", "x", []View{"logs"}, 1, 1_000_000); err == nil || !strings.Contains(err.Error(), "PSTACK_TOKEN") {
			t.Fatalf("empty key: %v", err)
		}
	})

	t.Run("the wire bytes: header and claims key order, seconds, unpadded base64url", func(t *testing.T) {
		// negative control: swap Dep and Views in the Claims struct → the body segment differs
		tok, _, _ := Sign(token, "pr-1", []View{"details"}, 1500, 1_000_000)
		parts := strings.Split(tok, ".")
		head, _ := js.B64URLDecode(parts[0])
		body, _ := js.B64URLDecode(parts[1])
		if string(head) != `{"alg":"HS256","typ":"JWT"}` {
			t.Fatalf("header %s", head)
		}
		// 1500 ms rounds UP to 2 s, as Math.ceil does.
		if string(body) != `{"sub":"share","dep":"pr-1","views":["details"],"iat":1000,"exp":1002}` {
			t.Fatalf("body %s", body)
		}
		if strings.Contains(tok, "=") {
			t.Fatal("padding in a base64url segment")
		}
	})

	t.Run("a token with unknown views only, a non-HS256 header, or a non-numeric exp is refused", func(t *testing.T) {
		// negative control: drop the `len(views) == 0` check → the unknown-views token verifies
		mk := func(head, body string) string {
			h, b := js.B64URL([]byte(head)), js.B64URL([]byte(body))
			return h + "." + b + "." + hmacB64(token, h+"."+b)
		}
		if Verify(token, mk(`{"alg":"HS256","typ":"JWT"}`, `{"sub":"share","dep":"d","views":["admin"],"iat":1,"exp":99}`), 2000) != nil {
			t.Fatal("unknown views verified")
		}
		if Verify(token, mk(`{"alg":"none","typ":"JWT"}`, `{"sub":"share","dep":"d","views":["logs"],"iat":1,"exp":99}`), 2000) != nil {
			t.Fatal("alg none verified")
		}
		if Verify(token, mk(`{"alg":"HS256","typ":"JWT"}`, `{"sub":"share","dep":"d","views":["logs"],"iat":1,"exp":"99"}`), 2000) != nil {
			t.Fatal("string exp verified")
		}
		if got := Verify(token, mk(`{"alg":"HS256","typ":"JWT"}`, `{"sub":"share","dep":"d","views":["logs","x"],"iat":1,"exp":99}`), 2000); got == nil || len(got.Views) != 1 {
			t.Fatalf("unknown views are filtered, not fatal: %+v", got)
		}
	})
}
