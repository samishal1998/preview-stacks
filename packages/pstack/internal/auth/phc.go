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

// The most a STORED hash may ask VerifyPassword to spend. The parameters come from the string, and
// login is unauthenticated, so without a ceiling one imported row pins the host: m is KiB (1 GiB,
// config's kdfMemoryCeiling), t is passes, and p over 255 truncates to uint8 0, which argon2 panics on.
const (
	maxHashMemory  = 1 << 20
	maxHashTime    = 16
	maxHashThreads = 255
)

// phc is a parsed argon2 PHC string.
type phc struct {
	id        bool // argon2id, else argon2i
	m, t, p   uint64
	salt, key []byte
}

// parsePHC reads `$argon2id$v=19$m=65536,t=2,p=1$salt$hash`; false for anything unparsable or over
// the ceilings above.
func parsePHC(encoded string) (h phc, ok bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return h, false
	}
	variant := parts[1]
	if variant != "argon2id" && variant != "argon2i" {
		return h, false
	}
	h.id = variant == "argon2id"
	if parts[2] != "v=19" {
		return h, false
	}
	var err error
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, _ := strings.Cut(kv, "=")
		var n uint64
		if n, err = strconv.ParseUint(v, 10, 32); err != nil {
			return h, false
		}
		switch k {
		case "m":
			h.m = n
		case "t":
			h.t = n
		case "p":
			h.p = n
		default:
			return h, false
		}
	}
	if h.m == 0 || h.t == 0 || h.p == 0 {
		return h, false
	}
	if h.m > maxHashMemory || h.t > maxHashTime || h.p > maxHashThreads {
		return h, false
	}
	if h.salt, err = rawStd.DecodeString(strings.TrimRight(parts[4], "=")); err != nil {
		return h, false
	}
	h.key, err = rawStd.DecodeString(strings.TrimRight(parts[5], "="))
	if err != nil || len(h.key) == 0 {
		return h, false
	}
	return h, true
}

// UsableHash is whether VerifyPassword could ever accept a password for this stored hash. Config
// import checks it so it never stores an account nobody can log into.
func UsableHash(encoded string) bool {
	_, ok := parsePHC(encoded)
	return ok
}

// VerifyPassword is `Bun.password.verify(password, encoded)`: the parameters come from the string,
// the compare is constant-time, and anything unparsable is simply false.
func VerifyPassword(password, encoded string) bool {
	h, ok := parsePHC(encoded)
	if !ok {
		return false
	}
	var got []byte
	if h.id {
		got = argon2.IDKey([]byte(password), h.salt, uint32(h.t), uint32(h.m), uint8(h.p), uint32(len(h.key)))
	} else {
		got = argon2.Key([]byte(password), h.salt, uint32(h.t), uint32(h.m), uint8(h.p), uint32(len(h.key)))
	}
	return subtle.ConstantTimeCompare(got, h.key) == 1
}
