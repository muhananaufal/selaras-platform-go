// Package deletion menghapus data profil saat akun dihapus.
package deletion

import (
	"context"
	"fmt"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Service adalah nama unit ini di dalam saga.
//
// Ia HARUS sama persis dengan salah satu nama di
// identity/domain.DeletionParticipants. Nama yang tidak cocok membuat
// konfirmasinya ditolak, dan saga menggantung selamanya menunggu unit yang
// sebenarnya sudah selesai.
const Service = "profile"

// Erase menghapus profil seorang pengguna.
//
// Berkunci user_id, bukan user_profile_id: id profil boleh kosong saat saga
// dimulai - profil yang tidak pernah dibuat adalah keadaan yang sah (B7) - dan
// unit ini justru pemiliknya, jadi ia tidak perlu menerjemahkan apa pun.
//
// IDEMPOTEN: baris yang tidak ada bukan galat. Relay outbox at-least-once, dan
// permintaan yang sama bisa tiba dua kali.
func Erase(ctx context.Context, q pg.Querier, userID, _ string) error {
	if _, err := q.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting the profile: %w", err)
	}
	return nil
}
