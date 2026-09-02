package revocation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/adapter/revocation"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/redis/redistest"
)

// stubSource berdiri untuk identity-svc: sumber kebenaran yang ditanya saat
// cache tidak tahu.
type stubSource struct {
	generation int64
	err        error
	calls      int
}

func (s *stubSource) CurrentGeneration(context.Context, domain.UserID) (int64, error) {
	s.calls++
	if s.err != nil {
		return 0, s.err
	}
	return s.generation, nil
}

func newChecker(t *testing.T, source *stubSource) (*revocation.RedisStore, domain.UserID) {
	t.Helper()

	client := redistest.Open(t)
	store, err := revocation.NewRedisStore(client, source, time.Minute)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}

	id, err := domain.NewUserID()
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	redistest.DeleteKeysFor(t, client, id.String())
	return store, id
}

func TestAPublishedGenerationIsRead(t *testing.T) {
	source := &stubSource{generation: 1}
	store, id := newChecker(t, source)
	ctx := context.Background()

	if err := store.PublishGeneration(ctx, id, 4); err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}

	ok, err := store.IsCurrent(ctx, id, 4)
	if err != nil {
		t.Fatalf("IsCurrent: %v", err)
	}
	if !ok {
		t.Error("the generation just published is not current")
	}
	if source.calls != 0 {
		t.Errorf("the source was consulted %d times; want 0, the cache had the answer", source.calls)
	}
}

func TestAnOlderGenerationIsRefused(t *testing.T) {
	source := &stubSource{generation: 7}
	store, id := newChecker(t, source)
	ctx := context.Background()

	if err := store.PublishGeneration(ctx, id, 7); err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}

	for _, gen := range []int64{1, 6} {
		ok, err := store.IsCurrent(ctx, id, gen)
		if err != nil {
			t.Fatalf("IsCurrent(%d): %v", gen, err)
		}
		if ok {
			t.Errorf("generation %d was accepted although %d is current", gen, 7)
		}
	}
}

// Generasi yang lebih BARU daripada yang tercatat juga ditolak. Ia hanya bisa
// muncul dari token yang dipalsukan atau dari cache yang tertinggal, dan
// keduanya bukan alasan untuk menerima.
func TestANewerGenerationIsAlsoRefused(t *testing.T) {
	source := &stubSource{generation: 3}
	store, id := newChecker(t, source)
	ctx := context.Background()

	if err := store.PublishGeneration(ctx, id, 3); err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}

	ok, err := store.IsCurrent(ctx, id, 99)
	if err != nil {
		t.Fatalf("IsCurrent: %v", err)
	}
	if ok {
		t.Error("a generation ahead of the recorded one was accepted")
	}
}

// Cache yang tidak tahu bertanya ke sumbernya, lalu mengingat jawabannya.
func TestAMissAsksTheSourceAndCachesTheAnswer(t *testing.T) {
	source := &stubSource{generation: 5}
	store, id := newChecker(t, source)
	ctx := context.Background()

	ok, err := store.IsCurrent(ctx, id, 5)
	if err != nil {
		t.Fatalf("IsCurrent: %v", err)
	}
	if !ok {
		t.Error("the current generation was refused after a cache miss")
	}
	if source.calls != 1 {
		t.Fatalf("the source was consulted %d times; want 1", source.calls)
	}

	if _, err := store.IsCurrent(ctx, id, 5); err != nil {
		t.Fatalf("second IsCurrent: %v", err)
	}
	if source.calls != 1 {
		t.Errorf("the source was consulted %d times; want 1, the answer should have been cached", source.calls)
	}
}

// ADR-020 mewajibkan gagal-tertutup. Sumber yang tidak terjangkau berarti
// pencabutan tidak bisa dibuktikan, dan menerima token dalam keadaan itu
// mengubah setiap gangguan menjadi jendela di mana logout tidak berlaku.
func TestAnUnreachableSourceFailsClosed(t *testing.T) {
	source := &stubSource{err: errors.New("identity-svc is unreachable")}
	store, id := newChecker(t, source)
	ctx := context.Background()

	ok, err := store.IsCurrent(ctx, id, 1)
	if err == nil {
		t.Fatal("IsCurrent succeeded although nothing could confirm the generation")
	}
	if ok {
		t.Error("IsCurrent returned true on a path that could not confirm anything")
	}
}

// Pengguna yang tidak dikenal sumbernya juga ditolak, bukan diterima.
func TestAnUnknownUserIsRefused(t *testing.T) {
	source := &stubSource{err: domain.ErrUserNotFound}
	store, id := newChecker(t, source)

	ok, err := store.IsCurrent(context.Background(), id, 1)
	if ok {
		t.Error("a token for an unknown user was accepted")
	}
	if err == nil {
		t.Error("IsCurrent reported no error for an unknown user")
	}
}

// Generasi di bawah satu tidak pernah sah: penghitungnya mulai dari satu.
func TestPublishingAnImpossibleGenerationIsRejected(t *testing.T) {
	store, id := newChecker(t, &stubSource{generation: 1})

	for _, gen := range []int64{0, -1} {
		if err := store.PublishGeneration(context.Background(), id, gen); err == nil {
			t.Errorf("PublishGeneration(%d) was accepted", gen)
		}
	}
}

func TestPublishingOverwritesTheEarlierValue(t *testing.T) {
	source := &stubSource{generation: 1}
	store, id := newChecker(t, source)
	ctx := context.Background()

	if err := store.PublishGeneration(ctx, id, 2); err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	if err := store.PublishGeneration(ctx, id, 3); err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}

	ok, err := store.IsCurrent(ctx, id, 2)
	if err != nil {
		t.Fatalf("IsCurrent: %v", err)
	}
	if ok {
		t.Error("the superseded generation is still accepted")
	}
	if source.calls != 0 {
		t.Error("the source was consulted although the cache held a value")
	}
}

func TestTheStoreSatisfiesBothDomainPorts(t *testing.T) {
	client := redistest.Open(t)
	store, err := revocation.NewRedisStore(client, &stubSource{}, time.Minute)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	var _ domain.RevocationChecker = store
	var _ domain.RevocationPublisher = store
}

func TestNewRedisStoreRefusesMissingDependencies(t *testing.T) {
	client := redistest.Open(t)

	if _, err := revocation.NewRedisStore(nil, &stubSource{}, time.Minute); err == nil {
		t.Error("accepted a nil client")
	}
	if _, err := revocation.NewRedisStore(client, nil, time.Minute); err == nil {
		t.Error("accepted a nil source")
	}
	if _, err := revocation.NewRedisStore(client, &stubSource{}, 0); err == nil {
		t.Error("accepted a zero cache lifetime")
	}
}
