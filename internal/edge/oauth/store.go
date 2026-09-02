// Package oauth memuat yang dibutuhkan gateway untuk alur masuk sosial:
// parameter state, dan penyerahan kode sekali pakai.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

var (
	// ErrUnknownState menandai callback yang state-nya tidak pernah kami
	// terbitkan, sudah dipakai, atau sudah kedaluwarsa. Ketiganya sama bagi
	// pemanggil.
	ErrUnknownState = errors.New("unknown or expired state")

	// ErrUnknownCode sama untuk kode penyerahan.
	ErrUnknownCode = errors.New("unknown or expired code")
)

const (
	statePrefix = "edge:oauth:state:"
	codePrefix  = "edge:oauth:handoff:"

	// secretBytes adalah 32 byte, 256 bit. Nilai-nilai ini melewati peramban
	// dan alamat URL, jadi menebaknya harus benar-benar mustahil.
	secretBytes = 32
)

// Store menyimpan nilai sekali pakai milik alur OAuth.
//
// Redis, bukan memori proses, dan itu bukan pilihan gaya: gateway berjalan
// dalam beberapa replica, dan callback dari penyedia bisa mendarat di replica
// mana pun. State yang disimpan di memori akan ditolak setiap kali callback
// tidak kebetulan kembali ke replica yang menerbitkannya.
type Store struct {
	client *goredis.Client

	// stateTTL pendek: ia hanya perlu bertahan selama pengguna berada di
	// halaman persetujuan penyedia.
	stateTTL time.Duration

	// codeTTL jauh lebih pendek lagi. Kodenya sempat melewati peramban, dan
	// satu-satunya yang menjaga jendela itu tetap sempit adalah masa
	// berlakunya.
	codeTTL time.Duration
}

func NewStore(client *goredis.Client, stateTTL, codeTTL time.Duration) (*Store, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil redis client")
	case stateTTL <= 0:
		return nil, errors.New("state lifetime must be positive")
	case codeTTL <= 0:
		return nil, errors.New("handoff code lifetime must be positive")
	}
	return &Store{client: client, stateTTL: stateTTL, codeTTL: codeTTL}, nil
}

// NewState menerbitkan parameter state dan mengingatnya.
//
// Menutup S11. Sistem lama memanggil Socialite dengan stateless(), yang
// mematikan verifikasi state di kedua sisi alur - sehingga callback-nya
// menerima kode dari mana pun. Penyerang bisa memaksa korban menyelesaikan
// alur masuk dengan akun Google milik penyerang, dan sejak itu segala yang
// dicatat korban masuk ke akun penyerang.
func (s *Store) NewState(ctx context.Context, provider string) (string, error) {
	value, err := secret()
	if err != nil {
		return "", err
	}
	// Nama penyedia ikut disimpan, sehingga state yang diterbitkan untuk satu
	// penyedia tidak bisa dipakai menyelesaikan alur penyedia lain.
	if err := s.client.Set(ctx, statePrefix+value, provider, s.stateTTL).Err(); err != nil {
		return "", fmt.Errorf("storing the oauth state: %w", err)
	}
	return value, nil
}

// ConsumeState memeriksa state dan langsung membuangnya.
//
// Pemeriksaan dan pembuangan berada dalam satu operasi atomik. Kalau
// terpisah, dua callback yang tiba bersamaan sama-sama akan lolos - dan
// state sekali pakai yang bisa dipakai dua kali bukan sekali pakai.
func (s *Store) ConsumeState(ctx context.Context, value, provider string) error {
	if value == "" {
		return ErrUnknownState
	}

	stored, err := s.client.GetDel(ctx, statePrefix+value).Result()
	if errors.Is(err, goredis.Nil) {
		return ErrUnknownState
	}
	if err != nil {
		return fmt.Errorf("reading the oauth state: %w", err)
	}
	if stored != provider {
		return fmt.Errorf("%w: issued for %q", ErrUnknownState, stored)
	}
	return nil
}

// NewHandoffCode menyimpan token akses di balik kode sekali pakai.
//
// Menutup S6. Sistem lama mengalihkan ke frontend dengan access_token di
// query string, dan query string masuk ke log server, riwayat peramban, dan
// header Referer. Yang diserahkan di sini hanyalah kode berumur detik, lewat
// fragment - yang bahkan tidak pernah dikirim ke server mana pun.
func (s *Store) NewHandoffCode(ctx context.Context, accessToken string) (string, error) {
	if accessToken == "" {
		return "", errors.New("refusing to hand off an empty token")
	}
	code, err := secret()
	if err != nil {
		return "", err
	}
	if err := s.client.Set(ctx, codePrefix+code, accessToken, s.codeTTL).Err(); err != nil {
		return "", fmt.Errorf("storing the handoff code: %w", err)
	}
	return code, nil
}

// ConsumeHandoffCode menukar kode dengan tokennya, sekali.
func (s *Store) ConsumeHandoffCode(ctx context.Context, code string) (string, error) {
	if code == "" {
		return "", ErrUnknownCode
	}

	token, err := s.client.GetDel(ctx, codePrefix+code).Result()
	if errors.Is(err, goredis.Nil) {
		return "", ErrUnknownCode
	}
	if err != nil {
		return "", fmt.Errorf("reading the handoff code: %w", err)
	}
	return token, nil
}

// secret menghasilkan nilai acak yang aman ditempelkan ke URL.
func secret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating a random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
