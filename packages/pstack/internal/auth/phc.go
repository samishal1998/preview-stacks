package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ── passwords: argon2id in PHC string format ─────────────────────────────────────────────────────
//
// `Bun.password.hash` wrote `$argon2id$v=19$m=65536,t=2,p=1$<salt>$<hash>` with raw (unpadded)
// standard base64 for the salt and the key. Verification parity does NOT depend on matching Bun's
// cost: variant, version, m, t and p are read OUT of the stored string and the key is recomputed
// with them, so a row hashed at m=19456,t=3 (the golden fixture's `dave`) verifies exactly as one
// at the default does. The constants below govern only NEW hashes.

const (
	hashMemory  = 65536
	hashTime    = 2
	hashThreads = 1
	hashSaltLen = 16
	hashKeyLen  = 32
)

var rawStd = base64.RawStdEncoding

// HashPassword is `Bun.password.hash(password, 'argon2id')`.
func HashPassword(password string) string {
	salt := make([]byte, hashSaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key := argon2.IDKey([]byte(password), salt, hashTime, hashMemory, hashThreads, hashKeyLen)
	return "$argon2id$v=19$m=" + strconv.Itoa(hashMemory) + ",t=" + strconv.Itoa(hashTime) + ",p=" + strconv.Itoa(hashThreads) +
		"$" + rawStd.EncodeToString(salt) + "$" + rawStd.EncodeToString(key)
}

// VerifyPassword is `Bun.password.verify(password, encoded)`: the parameters come from the string,
// the compare is constant-time, and anything unparsable is simply false.
func VerifyPassword(password, encoded string) bool {
	// $argon2id$v=19$m=65536,t=2,p=1$salt$hash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return false
	}
	variant := parts[1]
	if variant != "argon2id" && variant != "argon2i" {
		return false
	}
	if parts[2] != "v=19" {
		return false
	}
	var m, t, p uint64
	var err error
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, _ := strings.Cut(kv, "=")
		var n uint64
		if n, err = strconv.ParseUint(v, 10, 32); err != nil {
			return false
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			p = n
		default:
			return false
		}
	}
	if m == 0 || t == 0 || p == 0 {
		return false
	}
	salt, err := rawStd.DecodeString(strings.TrimRight(parts[4], "="))
	if err != nil {
		return false
	}
	want, err := rawStd.DecodeString(strings.TrimRight(parts[5], "="))
	if err != nil || len(want) == 0 {
		return false
	}
	var got []byte
	if variant == "argon2id" {
		got = argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	} else {
		got = argon2.Key([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
