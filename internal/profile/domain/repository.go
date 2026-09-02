package domain

import "context"

// ProfileRepository adalah port penyimpanan agregat Profile.
type ProfileRepository interface {
	// Create menyimpan profil baru. Pengguna yang sudah punya profil
	// menghasilkan ErrProfileExists - dan itu datang dari indeks unik, bukan
	// dari pemeriksaan pendahuluan, karena dua permintaan yang serempak akan
	// lolos di antara pembacaan dan penulisan.
	Create(ctx context.Context, p *Profile) error

	Update(ctx context.Context, p *Profile) error

	FindByID(ctx context.Context, id ProfileID) (*Profile, error)

	// FindByUserID dipakai identity-svc lewat ResolveProfileId. Profil yang
	// belum ada mengembalikan ErrProfileNotFound, dan pemanggilnya - yang
	// sedang menerbitkan token - memperlakukannya sebagai klaim kosong, bukan
	// sebagai galat (ADR-002 aturan 2).
	FindByUserID(ctx context.Context, userID UserID) (*Profile, error)
}
