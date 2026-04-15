package token

import (
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	key, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	claims := Claims{
		SessionID:  "sbx_test123",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(time.Hour).Unix(),
	}
	raw, err := Issue(key, claims)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, err := Validate(key, raw, time.Now())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.SessionID != claims.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, claims.SessionID)
	}
	if got.Repository != claims.Repository {
		t.Errorf("Repository = %q, want %q", got.Repository, claims.Repository)
	}
}

func TestExpiredToken(t *testing.T) {
	key, err := GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	claims := Claims{
		SessionID:  "sbx_expired",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(-time.Hour).Unix(),
	}
	raw, err := Issue(key, claims)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, err = Validate(key, raw, time.Now())
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestInvalidSignature(t *testing.T) {
	key1, _ := GenerateSigningKey()
	key2, _ := GenerateSigningKey()
	claims := Claims{
		SessionID:  "sbx_sig",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(time.Hour).Unix(),
	}
	raw, err := Issue(key1, claims)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, err = Validate(key2, raw, time.Now())
	if err == nil {
		t.Fatal("expected error for wrong signing key")
	}
}

func TestMalformedToken(t *testing.T) {
	key, _ := GenerateSigningKey()
	_, err := Validate(key, "not-a-valid-token", time.Now())
	if err == nil {
		t.Fatal("expected error for malformed token")
	}
}
