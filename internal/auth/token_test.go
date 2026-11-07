package auth

import (
	"testing"
	"time"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
)

func TestTokenRoundTrip(t *testing.T) {
	tm := NewTokenManager("test-secret-that-is-long-enough-1234567890", time.Hour)
	tok, exp, err := tm.Issue(Principal{UserID: 42, Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(exp) <= 0 {
		t.Fatalf("expiry not in the future: %v", exp)
	}
	p, err := tm.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != 42 || p.Role != RoleAdmin {
		t.Fatalf("got %+v", p)
	}
}

func TestTokenRejectsTamperingAndExpiry(t *testing.T) {
	a := NewTokenManager("secret-a-secret-a-secret-a-secret-a", time.Hour)
	b := NewTokenManager("secret-b-secret-b-secret-b-secret-b", time.Hour)
	tok, _, _ := a.Issue(Principal{UserID: 1, Role: RoleUser})

	if _, err := b.Parse(tok); !apperr.Is(err, apperr.KindUnauthorized) {
		t.Fatalf("token signed with another key must be rejected, got %v", err)
	}

	expired := NewTokenManager("secret-a-secret-a-secret-a-secret-a", -time.Minute)
	old, _, _ := expired.Issue(Principal{UserID: 1, Role: RoleUser})
	if _, err := a.Parse(old); !apperr.Is(err, apperr.KindUnauthorized) {
		t.Fatalf("expired token must be rejected, got %v", err)
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse battery") || CheckPassword(h, "wrong") {
		t.Fatal("password check mismatch")
	}
}
