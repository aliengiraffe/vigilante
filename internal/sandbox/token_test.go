package sandbox

import (
	"crypto/rand"
	"testing"
	"time"
)

func TestMintAndVerifyToken(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	token, err := MintToken(key, "sbx_abc123", "owner/repo", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}

	if err := VerifyToken(key, token); err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	token, err := MintToken(key, "sbx_abc123", "owner/repo", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}

	if err := VerifyToken(key, token); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestVerifyTokenWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	rand.Read(key1)
	rand.Read(key2)

	token, _ := MintToken(key1, "sbx_abc123", "owner/repo", time.Now().Add(time.Hour))

	if err := VerifyToken(key2, token); err == nil {
		t.Fatal("expected error for wrong signing key")
	}
}

func TestParseClaims(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	token, _ := MintToken(key, "sbx_test", "owner/myrepo", time.Now().Add(time.Hour))

	claims, err := ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.SessionID != "sbx_test" {
		t.Errorf("session_id = %q, want %q", claims.SessionID, "sbx_test")
	}
	if claims.Repository != "owner/myrepo" {
		t.Errorf("repository = %q, want %q", claims.Repository, "owner/myrepo")
	}
}

func TestParseMalformedToken(t *testing.T) {
	tests := []string{
		"",
		"onlyone",
		"two.parts",
		"not.valid.base64!!!",
	}
	for _, tok := range tests {
		if _, err := ParseToken(tok); err == nil {
			t.Errorf("ParseToken(%q): expected error", tok)
		}
	}
}
