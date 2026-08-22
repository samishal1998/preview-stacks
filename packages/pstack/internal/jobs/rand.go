package jobs

import (
	"crypto/rand"
)

func randByte() byte {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return b[0]
}
