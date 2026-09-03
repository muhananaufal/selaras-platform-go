// Package deletion menghapus proyeksi dasbor saat akun dihapus.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service adalah nama unit ini di dalam saga.
const Service = "dashboard"

// Erase menghapus read-model seorang pengguna.
//
// Read-model tidak memiliki satu fakta pun - seluruhnya salinan - tetapi
// salinan itu memuat persentase risiko, kategori kesehatan, dan riwayat
// analisis. "Hanya proyeksi" bukan alasan membiarkannya: yang tertinggal
// setelah akun dihapus tetap data pribadi, dan justru lebih sulit ditemukan
// karena tidak ada yang menganggapnya sumber.
//
// Riwayat lebih dulu, lalu barisnya: keduanya tidak dihubungkan foreign key -
// tabel ini sengaja tidak memakainya supaya proyeksi bisa menerima event dalam
// urutan apa pun - jadi urutannya dijaga di sini.
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	for _, table := range []string{"dashboard_assessments", "dashboards"} {
		if _, err := q.Exec(ctx, "DELETE FROM "+table+" WHERE user_id = $1", userID); err != nil {
			return fmt.Errorf("deleting %s: %w", table, err)
		}
	}
	return nil
}
