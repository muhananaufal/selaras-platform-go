package handler

import (
	"testing"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

func TestAnIdempotencyKeyIsBoundToItsUser(t *testing.T) {
	alice, bob := domain.Claims{}, domain.Claims{}
	var err error
	if alice.UserID, err = domain.ParseUserID(uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if bob.UserID, err = domain.ParseUserID(uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	a, b := idempotencyKeyFor(alice, "retry-1"), idempotencyKeyFor(bob, "retry-1")
	if a.GetValue() == b.GetValue() {
		t.Fatal("two users sharing a client key must not share a job key")
	}
	if a.GetValue() != alice.UserID.String()+"\x1f"+"retry-1" {
		t.Fatalf("unexpected shape %q", a.GetValue())
	}
	if idempotencyKeyFor(alice, "") != nil {
		t.Fatal("no header means no key, so the use case derives its own")
	}
}
