package app

import (
	"context"
	"log/slog"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// Dua langkah di bawah ini muncul di lebih dari satu use case, dan keduanya
// punya sifat yang sama: gagalnya tidak boleh membatalkan pekerjaan yang
// sudah tersimpan, tetapi juga tidak boleh hilang tanpa jejak. Ia dikumpulkan
// di sini supaya keputusan itu diambil sekali, bukan diulang - dan diulang
// berarti suatu saat ada satu tempat yang menelannya diam-diam.

// createProfileBestEffort meminta profile-svc membuat profil kosong, dan
// mengembalikan string kosong bila gagal.
//
// ADR-002 aturan 1: kegagalannya DILARANG menggagalkan pendaftaran. "Pengguna
// tanpa profil" sudah menjadi keadaan yang sah hari ini (B7) - jalur
// pendaftaran lewat Google di sistem lama tidak pernah membuat profil sama
// sekali, dan sistemnya tetap berjalan.
//
// Kegagalannya dicatat: tanpa catatan, profile-svc bisa mati berhari-hari dan
// yang terlihat hanya pengguna yang profilnya kosong tanpa sebab.
func createProfileBestEffort(ctx context.Context, profiles ProfileCreator, userID domain.UserID) string {
	profileID, err := profiles.CreateEmptyProfile(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "could not create an empty profile; sign-up continues",
			"user_id", userID.String(), "error", err)
		return ""
	}
	return profileID
}

// publishGenerationBestEffort mengumumkan generasi token yang baru ke
// pemeriksa pencabutan.
//
// Ia dipanggil SETELAH perubahannya tersimpan. Mengumumkan generasi yang
// gagal disimpan akan mengeluarkan pengguna dari sesinya berdasarkan
// perubahan yang tidak pernah terjadi.
//
// Sebaliknya, publikasi yang gagal tidak membatalkan apa pun: barisnya sudah
// tersimpan, jadi pencabutannya nyata, dan yang tertinggal hanya cache -
// pemeriksa yang meleset mengambilnya dari sumber aslinya.
func publishGenerationBestEffort(
	ctx context.Context,
	revocations domain.RevocationPublisher,
	userID domain.UserID,
	generation int64,
) {
	if err := revocations.PublishGeneration(ctx, userID, generation); err != nil {
		slog.ErrorContext(ctx, "could not publish the new token generation; "+
			"old tokens stay accepted until the checker reads from the source",
			"user_id", userID.String(), "generation", generation, "error", err)
	}
}
