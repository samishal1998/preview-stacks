// seal.go — the sealed envelope: scrypt for the key, AES-256-GCM for the payload.
//
// ── WHY SEALING IS THE CLI'S JOB AND NOT THE SERVER'S ────────────────────────────────────────────
//
// The API hands the document out in plaintext over an authenticated connection and never learns the
// passphrase. Sealing server-side would mean POSTing the passphrase to the box whose credentials are
// being exported — strictly worse than sealing where the file lands. So Seal/Unseal live here, are
// pure functions of (bytes, passphrase), and touch no store.
//
// ── CHOOSING N, r, p AGAINST THIS ARTIFACT ───────────────────────────────────────────────────────
//
// What a stolen envelope is worth if cracked: every account's argon2id hash (m=64 MiB, t=2 — so the
// passwords behind them still cost real money per guess), and, with no second factor at all, the
// registry passwords, the notifier signing secrets, the SSO client secret and every host secret.
// Those last four are the reason the seal has to carry the whole weight: they are plaintext the
// moment the envelope opens.
//
// The cost is therefore set by the WEAKEST box that must legitimately unseal, not by a laptop.
// `pstack cloud-init --config` unseals at first boot on the VM being provisioned, which may have
// 512 MiB–1 GiB of RAM. scrypt's footprint is 128·N·r bytes:
//
//	N=1<<15 (32 MiB)   too cheap — a GPU rig fits thousands of these in VRAM
//	N=1<<17 (128 MiB)  ~1 s here; a 24 GB GPU holds ~190 in parallel, not ~6000  ← chosen
//	N=1<<20 (1 GiB)    OOM-kills the small instance it was meant to provision
//
// So N=1<<17, r=8, p=1. p stays 1: raising it multiplies CPU without raising the memory floor that
// is doing the actual work against parallel hardware.
//
// The parameters are stored IN the envelope so changing these defaults later still opens old files.
// That means unseal reads N, r and p from an UNTRUSTED file and hands them to an allocator — a
// hostile envelope claiming N=1<<30 is a 128 GiB allocation and a dead process. checkKDF bounds
// them before scrypt is called; that guard is not optional.
//
// ── WHAT THE ENVELOPE DOES NOT AUTHENTICATE, AND WHY THAT IS FINE ────────────────────────────────
//
// The header is not passed as GCM additional data. It does not need to be: N, r, p and the salt are
// all key-derivation inputs, so flipping any of them derives a different key and the tag fails.
// `version` and `sealed` are checked before decryption and refused by name. There is no header bit
// whose modification could change the plaintext rather than destroy it.
//
// GCM cannot tell a wrong passphrase from a modified file — the outcome is one failed tag either
// way — so there is ONE error for both, and its text says both. An `ErrWrongPassphrase` that also
// fires on a truncated download sends the operator hunting the wrong thing.
package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/scrypt"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
)

// SealScheme names the construction. A future scheme is a new value, refused by name here rather
// than mis-parsed.
const SealScheme = "scrypt-aes256gcm"

// The defaults for a NEW seal. Old files carry their own — see checkKDF.
const (
	scryptN = 1 << 17
	scryptR = 8
	scryptP = 1
	saltLen = 16
	keyLen  = 32 // AES-256
	// cipher.NewGCM's standard nonce and tag sizes, so a malformed file can be refused without
	// paying for a key first.
	gcmNonceLen = 12
	gcmTagLen   = 16
)

// kdfMemoryCeiling is the most an envelope may ask us to allocate: 1 GiB. Above this a malformed or
// hostile file is a denial of service, and no legitimate writer needs it (the default is 128 MiB).
const kdfMemoryCeiling = 1 << 30

// ErrPassphrase is a failed GCM tag. Deliberately ONE error for two causes — see the header.
var ErrPassphrase = errors.New("could not open the sealed config: wrong passphrase, or the file was modified or truncated since it was sealed")

// KDF is the scrypt cost, stored so a change of defaults still opens an old file.
type KDF struct {
	N int `json:"n"`
	R int `json:"r"`
	P int `json:"p"`
	// Salt is standard base64. Fresh per seal.
	Salt string `json:"salt"`
}

// Envelope is the sealed file. Field order is the contract's, with pstackVersion appended: without
// a writer version OUTSIDE the seal, refusing an unknown `version` could not name what wrote it,
// which is the difference between "upgrade pstack" and a shrug.
type Envelope struct {
	Version int    `json:"version"`
	Sealed  string `json:"sealed"`
	KDF     KDF    `json:"kdf"`
	// Nonce is standard base64, 12 random bytes, FRESH PER SEAL. Never derived, never a counter:
	// two payloads sealed under one key and nonce leak their XOR and forge the tag.
	Nonce string `json:"nonce"`
	// Payload is standard base64 of the AES-256-GCM ciphertext with its 16-byte tag appended.
	Payload       string `json:"payload"`
	PstackVersion string `json:"pstackVersion"`
}

// Seal encrypts plaintext under passphrase and returns the envelope as JSON, ready to write.
func Seal(plaintext []byte, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, &Error{"a passphrase is required — an unsealed export is a plaintext copy of every credential on this host"}
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	gcm, err := aead([]byte(passphrase), salt, scryptN, scryptR, scryptP)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return jsonx.MarshalIndent(Envelope{
		Version:       FormatVersion,
		Sealed:        SealScheme,
		KDF:           KDF{N: scryptN, R: scryptR, P: scryptP, Salt: b64(salt)},
		Nonce:         b64(nonce),
		Payload:       b64(gcm.Seal(nil, nonce, plaintext, nil)),
		PstackVersion: version.Get(),
	})
}

// Unseal parses and decrypts. Everything it can tell apart BEFORE the tag check gets its own named
// error; everything it cannot is ErrPassphrase.
func Unseal(sealed []byte, passphrase string) ([]byte, error) {
	var env Envelope
	if err := json.Unmarshal(sealed, &env); err != nil {
		return nil, &Error{"this is not a sealed pstack config file: " + err.Error()}
	}
	if env.Version != FormatVersion {
		return nil, &Error{fmt.Sprintf("this file is sealed config version %d, written by %s; this is pstack %s, which understands version %d. Upgrade pstack to open it.",
			env.Version, env.writer(), version.Get(), FormatVersion)}
	}
	if env.Sealed != SealScheme {
		return nil, &Error{fmt.Sprintf(`this file is sealed with "%s" (written by %s); this pstack only knows "%s"`, env.Sealed, env.writer(), SealScheme)}
	}
	if err := checkKDF(env.KDF); err != nil {
		return nil, err
	}
	salt, err := unb64(env.KDF.Salt, "kdf.salt")
	if err != nil {
		return nil, err
	}
	nonce, err := unb64(env.Nonce, "nonce")
	if err != nil {
		return nil, err
	}
	payload, err := unb64(env.Payload, "payload")
	if err != nil {
		return nil, err
	}
	// Structural checks BEFORE the key derivation: a file that cannot possibly decrypt must not
	// cost a second of scrypt and 128 MiB first. cipher.NewGCM's defaults, asserted below.
	if len(nonce) != gcmNonceLen {
		return nil, &Error{fmt.Sprintf("the nonce is %d bytes; AES-GCM needs %d", len(nonce), gcmNonceLen)}
	}
	if len(payload) < gcmTagLen {
		return nil, &Error{"the payload is too short to be a sealed document — the file is truncated"}
	}
	gcm, err := aead([]byte(passphrase), salt, env.KDF.N, env.KDF.R, env.KDF.P)
	if err != nil {
		return nil, err
	}
	if gcm.NonceSize() != gcmNonceLen || gcm.Overhead() != gcmTagLen {
		// Unreachable with the stdlib's GCM; here so the constants above can never silently drift
		// from what the cipher actually wants.
		return nil, &Error{"the AES-GCM parameters are not what this build expects"}
	}
	plaintext, err := gcm.Open(nil, nonce, payload, nil)
	if err != nil {
		// The one place the two causes merge. Never wrap err: its text is "message authentication
		// failed", which reads as a bug rather than as "you typed the wrong passphrase".
		return nil, ErrPassphrase
	}
	return plaintext, nil
}

// writer names the pstack that wrote a file, for a refusal message.
func (e Envelope) writer() string {
	if e.PstackVersion == "" {
		return "an unknown pstack version"
	}
	return "pstack " + e.PstackVersion
}

// checkKDF bounds parameters that arrived in a file. scrypt.Key itself only rejects a non-power-of-2
// N; the memory it is about to allocate — 128·N·r bytes — is entirely the caller's problem, which
// makes an unchecked envelope a one-line remote OOM.
func checkKDF(k KDF) error {
	switch {
	case k.N < 1<<14 || k.N > 1<<20 || k.N&(k.N-1) != 0:
		return &Error{fmt.Sprintf("refusing kdf.n = %d — it must be a power of two between %d and %d", k.N, 1<<14, 1<<20)}
	case k.R < 1 || k.R > 16:
		return &Error{fmt.Sprintf("refusing kdf.r = %d — it must be between 1 and 16", k.R)}
	case k.P < 1 || k.P > 4:
		return &Error{fmt.Sprintf("refusing kdf.p = %d — it must be between 1 and 4", k.P)}
	case 128*int64(k.N)*int64(k.R) > kdfMemoryCeiling:
		return &Error{fmt.Sprintf("refusing kdf n=%d r=%d — opening it would allocate %d MiB, over the %d MiB ceiling",
			k.N, k.R, 128*int64(k.N)*int64(k.R)>>20, int64(kdfMemoryCeiling)>>20)}
	}
	return nil
}

// aead derives the key and returns the cipher. Split out so Seal and Unseal cannot drift.
func aead(passphrase, salt []byte, n, r, p int) (cipher.AEAD, error) {
	key, err := scrypt.Key(passphrase, salt, n, r, p, keyLen)
	if err != nil {
		return nil, &Error{"could not derive the key: " + err.Error()}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unb64(s, field string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, &Error{"the sealed file's " + field + " is not valid base64"}
	}
	return b, nil
}
