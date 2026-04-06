// Package token implements HMAC-signed sandbox session tokens.
//
// Each token encodes a session ID, target repository, and expiry timestamp.
// The reverse proxy validates these fields on every request from the gh
// mirror binary inside a sandbox container.
package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Claims holds the payload of a sandbox session token.
type Claims struct {
	SessionID  string `json:"sid"`
	Repository string `json:"repo"`
	ExpiresAt  int64  `json:"exp"`
}

// Expired reports whether the token's expiry timestamp is in the past
// relative to now.
func (c Claims) Expired(now time.Time) bool {
	return now.Unix() >= c.ExpiresAt
}

// GenerateSigningKey creates a cryptographically random 32-byte signing key.
func GenerateSigningKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	return key, nil
}

// Issue creates a signed token string for the given claims.
func Issue(key []byte, claims Claims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal token claims: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	sig := sign(key, encoded)
	return encoded + "." + sig, nil
}

// Validate parses and validates a signed token string.
// It verifies the HMAC signature and checks that the token has not expired.
func Validate(key []byte, raw string, now time.Time) (Claims, error) {
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 {
		return Claims{}, errors.New("malformed token")
	}
	encoded, sig := parts[0], parts[1]
	if !hmac.Equal([]byte(sign(key, encoded)), []byte(sig)) {
		return Claims{}, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Claims{}, fmt.Errorf("decode token payload: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, fmt.Errorf("unmarshal token claims: %w", err)
	}
	if claims.Expired(now) {
		return claims, errors.New("token expired")
	}
	return claims, nil
}

func sign(key []byte, data string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
