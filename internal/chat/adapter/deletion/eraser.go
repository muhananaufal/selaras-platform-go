// Package deletion menghapus percakapan saat akun dihapus.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service adalah nama unit ini di dalam saga.
const Service = "chat"

// Erase menghapus seluruh percakapan seorang pengguna.
//
// Pesannya ikut lewat ON DELETE CASCADE di basis data, bukan dihapus satu per
// satu di Go: yang kedua meninggalkan sisa saat prosesnya mati di tengah, dan
// sisa itu tidak akan pernah ditemukan siapa pun - tidak ada lagi percakapan
// yang menunjuk ke sana.
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	if _, err := q.Exec(ctx, `DELETE FROM conversations WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting the conversations: %w", err)
	}
	return nil
}
