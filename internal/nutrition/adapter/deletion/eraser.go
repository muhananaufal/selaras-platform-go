// Package deletion menghapus data kuliner saat akun dihapus.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service adalah nama unit ini di dalam saga.
const Service = "nutrition"

// Erase menghapus preferensi, panduan menu, dan bahasa yang di-cache.
//
// Ketiganya, bukan dua. Cache bahasa mudah terlupakan karena ia "hanya cache" -
// tetapi ia tetap baris yang berkunci user_id seseorang, dan sistem lama justru
// meninggalkan cache seperti ini utuh setelah akun dihapus (dua baris pembersih
// cache di DeleteUserAccountAction ditulis lalu dikomentari).
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	for _, table := range []string{
		"culinary_preferences",
		"daily_meal_guides",
		"user_languages",
	} {
		if _, err := q.Exec(ctx, "DELETE FROM "+table+" WHERE user_id = $1", userID); err != nil {
			return fmt.Errorf("deleting %s: %w", table, err)
		}
	}
	return nil
}
