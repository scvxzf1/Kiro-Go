package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

func generateCodeVerifier() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return GenerateAccountID()
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func generateCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
