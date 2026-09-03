// Package cache menyimpan bahasa pengguna yang datang lewat event.
//
// Ini CACHE, bukan sumber kebenaran: profile-svc tetap pemiliknya. Yang di sini
// boleh basi, boleh hilang, dan boleh dibangun ulang dari awal topic.
package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// DefaultLanguage dipakai saat bahasa pengguna belum diketahui.
//
// Sama dengan bawaan sistem lama. Bahasa yang tidak diketahui menghasilkan
// panduan dalam bahasa ini, bukan kegagalan: seseorang yang belum pernah
// menyentuh profilnya tetap berhak mendapat saran menu.
const DefaultLanguage = "id"

// Languages membaca dan menulis bahasa yang di-cache.
type Languages struct {
	db pg.Querier
}

func NewLanguages(db pg.Querier) *Languages { return &Languages{db: db} }

// Of mengembalikan bahasa seorang pengguna, atau bawaannya.
//
// Ia TIDAK pernah mengembalikan galat karena cache-nya kosong. Cache yang
// kosong adalah keadaan normal - konsumen belum menyusul, atau pengguna belum
// pernah menyimpan profilnya - dan menjadikannya galat akan menghentikan
// pembuatan panduan karena sebuah salinan yang boleh hilang.
//
// Galat basis data yang sungguhan tetap dikembalikan: itu bukan cache yang
// kosong, itu penyimpanan yang bermasalah, dan mendiamkannya berarti setiap
// pengguna diam-diam mendapat bahasa bawaan tanpa ada yang tahu.
func (l *Languages) Of(ctx context.Context, userID string) (string, error) {
	const q = `SELECT language FROM user_languages WHERE user_id = $1`

	var language string
	switch err := l.db.QueryRow(ctx, q, userID).Scan(&language); {
	case errors.Is(err, pgx.ErrNoRows):
		return DefaultLanguage, nil
	case err != nil:
		return "", fmt.Errorf("reading the cached language: %w", err)
	}

	if strings.TrimSpace(language) == "" {
		return DefaultLanguage, nil
	}
	return language, nil
}

// Remember menyimpan bahasa dari sebuah event.
//
// Event yang LEBIH TUA dari yang sudah tersimpan diabaikan. Kafka menjamin
// urutan per partisi, tetapi partisi bisa berubah dan konsumen bisa diputar
// ulang - tanpa penjagaan ini, pemutaran ulang akan mengembalikan bahasa lama
// seseorang berbulan-bulan setelah ia menggantinya.
func (l *Languages) Remember(ctx context.Context, userID, language string, observedAt time.Time) error {
	if strings.TrimSpace(language) == "" {
		language = DefaultLanguage
	}

	const q = `
		INSERT INTO user_languages (user_id, language, observed_at, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id) DO UPDATE
		SET language = EXCLUDED.language,
		    observed_at = EXCLUDED.observed_at,
		    updated_at = now()
		WHERE user_languages.observed_at < EXCLUDED.observed_at`

	if _, err := l.db.Exec(ctx, q, userID, language, observedAt); err != nil {
		return fmt.Errorf("caching the language: %w", err)
	}
	return nil
}

// Forget menghapus cache seorang pengguna.
//
// Dipakai saga penghapusan akun: salinan yang tertinggal setelah akun dihapus
// adalah data pribadi yang tidak seorang pun tahu masih ada.
func (l *Languages) Forget(ctx context.Context, userID string) error {
	if _, err := l.db.Exec(ctx, `DELETE FROM user_languages WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("forgetting the cached language: %w", err)
	}
	return nil
}
