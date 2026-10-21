// Package auth creates high-entropy bearer credentials. Only SHA-256 digests
// cross the persistence boundary; passwords are not part of this protocol.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

func New(kind string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return kind + "_" + base64.RawURLEncoding.EncodeToString(b), nil
}
func Hash(token string) string { s := sha256.Sum256([]byte(token)); return hex.EncodeToString(s[:]) }
func Bearer(header, kind string) string {
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	s := strings.TrimPrefix(header, "Bearer ")
	if !strings.HasPrefix(s, kind+"_") || len(s) != len(kind)+44 {
		return ""
	}
	return s
}
