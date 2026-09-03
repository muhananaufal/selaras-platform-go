// Package deletion menghapus program coaching saat akun dihapus.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service adalah nama unit ini di dalam saga.
const Service = "coaching"

// Erase menghapus seluruh program coaching seorang pengguna.
//
// Pekan, tugas, thread, dan pesannya ikut lewat ON DELETE CASCADE. Menghapus
// kelimanya satu per satu di Go akan meninggalkan sisa saat prosesnya mati di
// tengah - dan sisa itu tidak bisa ditemukan lagi, karena programnya yang
// menjadi satu-satunya jalan ke sana sudah hilang.
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	if _, err := q.Exec(ctx, `DELETE FROM coaching_programs WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting the coaching programs: %w", err)
	}
	return nil
}
