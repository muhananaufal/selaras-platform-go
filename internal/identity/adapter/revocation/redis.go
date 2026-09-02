// Package revocation menjawab apakah sebuah token masih berada di generasi
// yang berlaku bagi pemiliknya.
package revocation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// GenerationSource adalah sumber kebenaran saat cache tidak tahu.
//
// Di edge, implementasinya adalah klien gRPC ke identity-svc - satu-satunya
// yang boleh membaca skema identity. Ia sengaja bukan koneksi basis data:
// isolasi skema-per-service ditegakkan oleh basis datanya sendiri, dan edge
// tidak punya hak di sana.
type GenerationSource interface {
	CurrentGeneration(ctx context.Context, userID domain.UserID) (int64, error)
}

// keyPrefix menamai ruang kunci milik alur ini, supaya ia tidak bertabrakan
// dengan pemakaian Redis lain di service yang sama.
const keyPrefix = "identity:token-generation:"

// RedisStore memenuhi domain.RevocationChecker dan domain.RevocationPublisher.
//
// Redis di sini adalah cache, bukan sumber kebenaran. Sumbernya tetap kolom
// token_generation di basis data identity; yang disimpan di sini hanya
// salinan, supaya pemeriksaan di setiap request tidak menjadi panggilan ke
// identity-svc di setiap request (ADR-020 keputusan 3).
type RedisStore struct {
	client *goredis.Client
	source GenerationSource

	// ttl membatasi berapa lama sebuah salinan yang tertinggal bisa hidup.
	//
	// Ia adalah tawar-menawar yang harus dipilih sadar. Publikasi yang gagal
	// meninggalkan generasi lama di cache, dan token yang seharusnya sudah
	// dicabut tetap diterima sampai salinan itu kedaluwarsa - jadi TTL yang
	// panjang memperpanjang jendela itu. TTL yang pendek mempersempitnya
	// tetapi mengirim lebih banyak permintaan ke identity-svc.
	ttl time.Duration
}

func NewRedisStore(client *goredis.Client, source GenerationSource, ttl time.Duration) (*RedisStore, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case source == nil:
		return nil, errors.New("nil generation source")
	case ttl <= 0:
		return nil, errors.New("cache lifetime must be positive")
	}
	return &RedisStore{client: client, source: source, ttl: ttl}, nil
}

var (
	_ domain.RevocationChecker   = (*RedisStore)(nil)
	_ domain.RevocationPublisher = (*RedisStore)(nil)
)

func key(userID domain.UserID) string { return keyPrefix + userID.String() }

// IsCurrent menjawab apakah generation masih generasi yang berlaku.
//
// GAGAL-TERTUTUP, dan itu wajib (ADR-020). Setiap jalur yang tidak bisa
// memastikan generasi yang berlaku mengembalikan galat, bukan true.
// Menerima token dalam keadaan itu akan mengubah setiap gangguan - Redis
// mati, identity-svc mati, jaringan putus - menjadi jendela di mana logout
// dan reset kata sandi tidak berlaku sama sekali.
func (s *RedisStore) IsCurrent(ctx context.Context, userID domain.UserID, generation int64) (bool, error) {
	current, err := s.client.Get(ctx, key(userID)).Int64()

	switch {
	case err == nil:
		return current == generation, nil

	case errors.Is(err, goredis.Nil):
		// Cache tidak tahu. Itu keadaan biasa - salinannya kedaluwarsa, atau
		// pengguna ini belum pernah diperiksa di replica ini.
		return s.askSource(ctx, userID, generation)

	default:
		// Redis bermasalah. Sumbernya masih bisa ditanya, jadi ditanya:
		// gangguan cache tidak boleh langsung menjadi pemadaman autentikasi.
		// Kalau sumbernya juga gagal, barulah galat - dan itu penolakan.
		return s.askSource(ctx, userID, generation)
	}
}

func (s *RedisStore) askSource(ctx context.Context, userID domain.UserID, generation int64) (bool, error) {
	current, err := s.source.CurrentGeneration(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("cannot confirm the token generation: %w", err)
	}

	// Jawabannya diingat, tetapi kegagalan mengingat tidak membatalkan
	// jawabannya - yang hilang hanya kecepatan permintaan berikutnya, bukan
	// kebenarannya. Ia tetap dicatat: cache yang diam-diam berhenti bekerja
	// terlihat sebagai beban yang naik pelan di identity-svc, dan itu jauh
	// lebih sulit dilacak daripada satu baris log.
	if cacheErr := s.write(ctx, userID, current); cacheErr != nil {
		slog.WarnContext(ctx, "could not cache the token generation",
			"user_id", userID.String(), "error", cacheErr)
	}
	return current == generation, nil
}

// PublishGeneration menyimpan generasi yang berlaku.
func (s *RedisStore) PublishGeneration(ctx context.Context, userID domain.UserID, generation int64) error {
	// Penghitungnya mulai dari satu, jadi nilai di bawah itu hanya bisa
	// datang dari kekeliruan pemanggil. Menyimpannya akan menolak setiap
	// token milik pengguna itu sampai salinannya kedaluwarsa.
	if generation < 1 {
		return fmt.Errorf("token generation %d is impossible; the counter starts at 1", generation)
	}
	return s.write(ctx, userID, generation)
}

func (s *RedisStore) write(ctx context.Context, userID domain.UserID, generation int64) error {
	if err := s.client.Set(ctx, key(userID), strconv.FormatInt(generation, 10), s.ttl).Err(); err != nil {
		return fmt.Errorf("caching token generation: %w", err)
	}
	return nil
}
