package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

const pass = "correct horse battery staple"

const fixtureDoc = `{"version":1,"secret":"hunter2"}`

// One seal, shared by every test that just needs A valid envelope to damage. Deriving a key costs
// ~1 s and 128 MiB by design (see seal.go), so sealing once per assertion would make this package
// the slowest in the repo for no extra coverage.
var (
	fixtureOnce sync.Once
	fixtureVal  []byte
)

func fixture(t *testing.T) []byte {
	t.Helper()
	fixtureOnce.Do(func() { fixtureVal = seal(t, fixtureDoc) })
	if fixtureVal == nil {
		t.Fatal("the shared fixture could not be sealed")
	}
	return fixtureVal
}

func seal(t *testing.T, plaintext string) []byte {
	t.Helper()
	b, err := Seal([]byte(plaintext), pass)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// parse/re-marshal an envelope so a test can modify one field.
func edit(t *testing.T, sealed []byte, fn func(*Envelope)) []byte {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(sealed, &env); err != nil {
		t.Fatal(err)
	}
	fn(&env)
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// negative control: make `nonce` a fixed slice in Seal (`nonce := make([]byte, gcm.NonceSize())`
// with no rand.Read) — the two seals become byte-identical and the nonce assertion fails.
func TestSealRoundTripAndFreshNonce(t *testing.T) {
	doc := fixtureDoc
	a, b := fixture(t), seal(t, doc)

	if bytes.Contains(a, []byte("hunter2")) {
		t.Fatalf("the plaintext is in the sealed file: %s", a)
	}
	var ea, eb Envelope
	if err := json.Unmarshal(a, &ea); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &eb); err != nil {
		t.Fatal(err)
	}
	// A reused (nonce, key) pair leaks the XOR of two payloads and lets a tag be forged. Both
	// halves are fresh per seal, so neither can repeat.
	if ea.Nonce == eb.Nonce || ea.KDF.Salt == eb.KDF.Salt || ea.Payload == eb.Payload {
		t.Fatalf("a seal repeated itself:\n%s\n%s", a, b)
	}
	if ea.Version != FormatVersion || ea.Sealed != SealScheme || ea.PstackVersion == "" {
		t.Fatalf("envelope header: %+v", ea)
	}
	if ea.KDF.N != scryptN || ea.KDF.R != scryptR || ea.KDF.P != scryptP {
		t.Fatalf("kdf not recorded: %+v", ea.KDF)
	}
	for _, sealed := range [][]byte{a, b} {
		got, err := Unseal(sealed, pass)
		if err != nil || string(got) != doc {
			t.Fatalf("unseal: %q %v", got, err)
		}
	}
}

// negative control: in Unseal, `return payload, nil` when gcm.Open fails — the assertion that no
// plaintext comes back, and the ErrPassphrase assertion, both fail.
func TestWrongPassphrase(t *testing.T) {
	got, err := Unseal(fixture(t), "not the passphrase")
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("err = %v, want ErrPassphrase", err)
	}
	if got != nil {
		t.Fatalf("garbage returned alongside the error: %q", got)
	}
	// One error for two causes, and the text has to say both — see seal.go's header.
	if !strings.Contains(err.Error(), "wrong passphrase") || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("message names only one cause: %v", err)
	}
}

func TestCorruptionAndTampering(t *testing.T) {
	t.Run("a truncated payload is named, not decrypted", func(t *testing.T) {
		// negative control: delete the `len(payload) < gcmTagLen` guard in Unseal — the failure
		// becomes ErrPassphrase, whose text ALSO contains the word "truncated", which is why this
		// asserts on the guard's own sentence and not on that word.
		sealed := edit(t, fixture(t), func(e *Envelope) {
			raw, _ := base64.StdEncoding.DecodeString(e.Payload)
			e.Payload = base64.StdEncoding.EncodeToString(raw[:4])
		})
		_, err := Unseal(sealed, pass)
		if err == nil || !strings.Contains(err.Error(), "too short to be a sealed document") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a flipped ciphertext byte is caught by GCM", func(t *testing.T) {
		// negative control: replace the gcm.Open call in Unseal with `return payload, nil` — the
		// corrupted bytes come back with no error and this fails.
		sealed := edit(t, fixture(t), func(e *Envelope) {
			raw, _ := base64.StdEncoding.DecodeString(e.Payload)
			raw[0] ^= 0x01
			e.Payload = base64.StdEncoding.EncodeToString(raw)
		})
		if _, err := Unseal(sealed, pass); !errors.Is(err, ErrPassphrase) {
			t.Fatalf("err = %v, want ErrPassphrase", err)
		}
	})

	t.Run("a rewritten kdf cost cannot open the payload", func(t *testing.T) {
		// negative control: derive the key from the constants (scryptN, scryptR, scryptP) instead of
		// from env.KDF in Unseal — the rewritten n is then ignored and the file opens.
		sealed := edit(t, fixture(t), func(e *Envelope) { e.KDF.N = 1 << 15 })
		if _, err := Unseal(sealed, pass); !errors.Is(err, ErrPassphrase) {
			t.Fatalf("err = %v, want ErrPassphrase", err)
		}
	})

	t.Run("a swapped salt cannot open the payload", func(t *testing.T) {
		// negative control: same mutation as above (a constant salt in Unseal) — the swap stops
		// mattering and the file opens.
		sealed := edit(t, fixture(t), func(e *Envelope) {
			raw, _ := base64.StdEncoding.DecodeString(e.KDF.Salt)
			raw[0] ^= 0xff
			e.KDF.Salt = base64.StdEncoding.EncodeToString(raw)
		})
		if _, err := Unseal(sealed, pass); !errors.Is(err, ErrPassphrase) {
			t.Fatalf("err = %v, want ErrPassphrase", err)
		}
	})

	t.Run("a mangled base64 field is named, not decrypted", func(t *testing.T) {
		// negative control: ignore unb64's error (`b, _ := base64.StdEncoding.DecodeString(s)`) —
		// the nonce becomes empty, the failure becomes a nonce-size error, and this fails.
		sealed := edit(t, fixture(t), func(e *Envelope) { e.Nonce = "!!!! not base64" })
		_, err := Unseal(sealed, pass)
		if err == nil || !strings.Contains(err.Error(), "nonce is not valid base64") {
			t.Fatalf("err = %v", err)
		}
	})
}

// negative control: delete the `checkKDF(env.KDF)` call in Unseal — r=64 is accepted by scrypt, a
// key is derived, and the failure becomes ErrPassphrase instead of the named refusal.
//
// r=64 is used rather than a hostile n because this test RUNS the mutation: n=1<<30 without the
// guard would allocate 128 GiB. The n ceiling is proved against checkKDF directly, below, where
// nothing is allocated at all.
func TestHostileKDFParametersRefused(t *testing.T) {
	sealed := edit(t, fixture(t), func(e *Envelope) { e.KDF.R = 64 })
	_, err := Unseal(sealed, pass)
	if err == nil || !strings.Contains(err.Error(), "refusing kdf.r = 64") {
		t.Fatalf("err = %v", err)
	}
	if !IsError(err) {
		t.Fatalf("err = %T, want *Error", err)
	}
}

// negative control: widen the n bound to `k.N > 1<<31` in checkKDF — {N: 1<<21, R: 1} is accepted
// and this fails. That case exists BECAUSE the obvious one does not work: N=1<<30 is refused by the
// memory ceiling as well, so widening the range alone would leave it refused and prove nothing.
// checkKDF is a pure function, so running this control allocates nothing.
func TestKDFCeilingIsCheckedBeforeAllocating(t *testing.T) {
	ok := KDF{N: scryptN, R: scryptR, P: scryptP}
	if err := checkKDF(ok); err != nil {
		t.Fatalf("the defaults must pass: %v", err)
	}
	// 128·N·r bytes: 1<<30 with r=8 is 1 TiB, which is a dead process, not a slow one.
	for _, bad := range []KDF{
		{N: 1 << 30, R: 8, P: 1}, // 1 TiB: the memory ceiling
		{N: 1 << 21, R: 1, P: 1}, // 256 MiB — under the ceiling, over the n range
		{N: 1000, R: 8, P: 1},    // not a power of two
		{N: 1 << 10, R: 8, P: 1}, // below the floor: a seal that cheap is not a seal
		{N: scryptN, R: 0, P: 1},
		{N: scryptN, R: 8, P: 99},
		{},
	} {
		if err := checkKDF(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

// negative control: drop the `env.Version != FormatVersion` check in Unseal — the file is then run
// through the cipher and fails as ErrPassphrase, naming nothing.
func TestUnknownVersionNamesTheWriter(t *testing.T) {
	sealed := edit(t, fixture(t), func(e *Envelope) {
		e.Version = 2
		e.PstackVersion = "9.9.9"
	})
	_, err := Unseal(sealed, pass)
	if err == nil || !strings.Contains(err.Error(), "pstack 9.9.9") || !strings.Contains(err.Error(), "Upgrade pstack") {
		t.Fatalf("err = %v", err)
	}

	// Same for a scheme this build does not know.
	sealed = edit(t, fixture(t), func(e *Envelope) { e.Sealed = "argon2-chacha20poly1305" })
	if _, err := Unseal(sealed, pass); err == nil || !strings.Contains(err.Error(), "argon2-chacha20poly1305") {
		t.Fatalf("err = %v", err)
	}
}

// negative control: drop the empty-passphrase guard in Seal — an unsealed-in-effect file is written
// and this fails.
func TestSealRefusesAnEmptyPassphrase(t *testing.T) {
	if _, err := Seal([]byte(`{}`), ""); err == nil || !strings.Contains(err.Error(), "passphrase is required") {
		t.Fatalf("err = %v", err)
	}
}
