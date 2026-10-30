package mfastore_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/mfastore"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/redis/redistest"
)

func TestTicketsCountAttemptsAndExpire(t *testing.T) {
	client := redistest.Open(t)
	store, err := mfastore.NewTickets(client, 2*time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}

	ticket, err := store.Create(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// The key in Redis is a digest, not the ticket a client presents.
	keys, err := client.Keys(ctx, "identity:mfa-ticket:*").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if strings.Contains(k, ticket) {
			t.Fatal("the ticket itself is a Redis key")
		}
	}

	for i := 1; i <= 3; i++ {
		got, err := store.Use(ctx, ticket)
		if err != nil || got != id {
			t.Fatalf("attempt %d = %v, %v; want %v", i, got, err, id)
		}
	}
	if _, err := store.Use(ctx, ticket); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("the attempt past the budget = %v; want ErrInvalidSecondFactor", err)
	}

	// An unknown ticket is not created by trying it.
	if _, err := store.Use(ctx, "invented"); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("an invented ticket = %v", err)
	}
	if n, _ := client.Exists(ctx, "identity:mfa-ticket:invented").Result(); n != 0 {
		t.Fatal("trying an invented ticket created a key")
	}

	expiring, err := store.Create(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := store.Use(ctx, expiring); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("an expired ticket = %v", err)
	}

	deleted, err := store.Create(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Use(ctx, deleted); !errors.Is(err, domain.ErrInvalidSecondFactor) {
		t.Fatalf("a deleted ticket = %v", err)
	}
}
