package sandbox

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TokenClaims are the payload of a sandbox API token.
type TokenClaims struct {
	SessionID  string `json:"sid"`
	Repository string `json:"repo"`
	ExpiresAt  int64  `json:"exp"`
}

// MintToken creates an HMAC-SHA256 signed token encoding the given claims.
// The token format is base64(header).base64(claims).base64(signature).
func MintToken(signingKey []byte, sessionID string, repo string, expiresAt time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))

	claims := TokenClaims{
		SessionID:  sessionID,
		Repository: repo,
		ExpiresAt:  expiresAt.Unix(),
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal token claims: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)

	message := header + "." + payload
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(message))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return message + "." + sig, nil
}

// ParseToken decodes the claims from a token without verifying the signature.
// Use VerifyToken for full validation.
func ParseToken(tokenStr string) (*TokenClaims, error) {
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed token: expected 3 parts, got %d", len(parts))
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode token claims: %w", err)
	}

	var claims TokenClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("unmarshal token claims: %w", err)
	}

	return &claims, nil
}

// VerifyToken checks the HMAC signature and TTL of a token.
func VerifyToken(signingKey []byte, tokenStr string) error {
	parts := strings.SplitN(tokenStr, ".", 3)
	if len(parts) != 3 {
		return fmt.Errorf("malformed token")
	}

	message := parts[0] + "." + parts[1]
	providedSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(message))
	expectedSig := mac.Sum(nil)

	if !hmac.Equal(providedSig, expectedSig) {
		return fmt.Errorf("signature mismatch")
	}

	claims, err := ParseToken(tokenStr)
	if err != nil {
		return err
	}

	if time.Now().Unix() > claims.ExpiresAt {
		return fmt.Errorf("token expired")
	}

	return nil
}
