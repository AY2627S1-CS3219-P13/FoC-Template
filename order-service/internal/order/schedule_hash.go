package order

import (
	"crypto/sha256"
	"encoding/hex"
)

func importFingerprint(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
