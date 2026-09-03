// Package deletion menghapus penilaian risiko saat akun dihapus.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service adalah nama unit ini di dalam saga.
const Service = "assessment"

// Erase menghapus penilaian dan cuplikan profil seorang pengguna.
//
// Unit ini punya DUA kunci pemilik, dan itu bukan kelalaian: penilaian berkunci
// user_profile_id karena itu pemilik agregatnya, sementara cache profil berkunci
// user_id karena itu identitas yang terverifikasi di setiap permintaan (F2-16).
// Keduanya harus dihapus.
//
// userProfileID BOLEH kosong. Itu terjadi saat profil pengguna tidak bisa
// ditemukan waktu saga dimulai - keadaan yang sah (B7). Dalam hal itu tidak ada
// penilaian yang bisa dihapus, dan yang benar adalah TIDAK menghapus apa pun,
// bukan menghapus dengan kunci kosong. Kunci kosong pada kolom UUID akan
// ditolak Postgres, dan penolakan itu akan menggagalkan seluruh saga untuk
// pengguna yang memang tidak punya penilaian.
func Erase(ctx context.Context, q pg.Querier, userID, userProfileID string) error {
	if userProfileID != "" {
		if _, err := q.Exec(ctx,
			`DELETE FROM risk_assessments WHERE user_profile_id = $1`, userProfileID); err != nil {
			return fmt.Errorf("deleting the risk assessments: %w", err)
		}
	}

	// Cuplikan profil adalah SALINAN data pribadi - tanggal lahir, jenis
	// kelamin, negara. Salinan yang tertinggal setelah akun dihapus adalah data
	// pribadi yang tidak seorang pun tahu masih ada.
	if _, err := q.Exec(ctx,
		`DELETE FROM profile_snapshots WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting the cached profile snapshot: %w", err)
	}
	return nil
}
