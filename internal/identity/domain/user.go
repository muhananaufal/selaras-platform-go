package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidRole         = errors.New("invalid role")
	ErrInvalidUserID       = errors.New("invalid user id")
	ErrEmptyPasswordHash   = errors.New("empty password hash")
	ErrGoogleAlreadyLinked = errors.New("a different google account is already linked")
)

// Role adalah tipe tersendiri, bukan string, supaya "amdin" gagal saat
// kompilasi atau di konstruktor - bukan diam-diam menjadi peran yang tidak
// dikenali di tengah pemeriksaan otorisasi.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

func NewRole(raw string) (Role, error) {
	switch r := Role(strings.ToLower(strings.TrimSpace(raw))); r {
	case RoleUser, RoleAdmin:
		return r, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, raw)
	}
}

func (r Role) String() string { return string(r) }

// UserID adalah UUIDv7: terurut menurut waktu, sehingga penyisipan tetap
// berkumpul di ujung indeks, tetapi tidak bisa ditebak berurutan seperti
// bigint auto-increment yang dipakai sistem lama.
type UserID struct{ v uuid.UUID }

func NewUserID() (UserID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return UserID{}, fmt.Errorf("generating user id: %w", err)
	}
	return UserID{v: v}, nil
}

func ParseUserID(raw string) (UserID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("%w: %q", ErrInvalidUserID, raw)
	}
	return UserID{v: v}, nil
}

func (id UserID) String() string { return id.v.String() }
func (id UserID) IsZero() bool   { return id.v == uuid.Nil }

// UserState adalah bentuk datar sebuah User untuk menyeberangi batas
// penyimpanan. Repository memakainya untuk membaca dan menulis tanpa
// mengintip ke dalam agregat, dan tanpa User terpaksa membuka bidangnya.
type UserState struct {
	ID              UserID
	Email           Email
	Role            Role
	PasswordHash    PasswordHash
	GoogleID        string
	EmailVerifiedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

// User adalah agregat identitas: siapa yang boleh masuk, dan dengan cara apa.
// Ia sengaja tidak tahu apa-apa tentang profil - nama, tanggal lahir, dan
// wilayah ada di unit lain (ADR-002).
type User struct {
	state UserState
}

// Register membuat akun berbasis kata sandi.
//
// Verifikasi email sengaja tidak diberikan: pada titik ini belum ada apa pun
// yang membuktikan alamat itu milik si pendaftar.
func Register(email Email, hash PasswordHash, now time.Time) (*User, error) {
	if hash == "" {
		return nil, ErrEmptyPasswordHash
	}
	id, err := NewUserID()
	if err != nil {
		return nil, err
	}
	return &User{state: UserState{
		ID:           id,
		Email:        email,
		Role:         RoleUser,
		PasswordHash: hash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}}, nil
}

// RegisterWithGoogle membuat akun yang memang tidak punya kata sandi.
//
// Sistem lama menyimpan hash dari 32 karakter acak untuk mengisi kolom yang
// NOT NULL. Hash itu berbohong: ia menyatakan ada kredensial yang bisa
// dipakai, padahal tidak ada. Di sini ketiadaan kata sandi dinyatakan apa
// adanya, dan alur reset kata sandi bisa membedakan keduanya.
func RegisterWithGoogle(email Email, googleID string, now time.Time) (*User, error) {
	if strings.TrimSpace(googleID) == "" {
		return nil, errors.New("empty google id")
	}
	id, err := NewUserID()
	if err != nil {
		return nil, err
	}
	verified := now
	return &User{state: UserState{
		ID:              id,
		Email:           email,
		Role:            RoleUser,
		GoogleID:        googleID,
		EmailVerifiedAt: &verified,
		CreatedAt:       now,
		UpdatedAt:       now,
	}}, nil
}

// Hydrate menyusun ulang User dari penyimpanan tanpa melewati aturan
// konstruktor - baris yang sudah tersimpan adalah fakta, bukan permintaan
// yang perlu divalidasi ulang.
func Hydrate(s UserState) *User { return &User{state: s} }

// State menyalin keadaan keluar untuk penyimpanan.
func (u *User) State() UserState { return u.state }

func (u *User) ID() UserID                 { return u.state.ID }
func (u *User) Email() Email               { return u.state.Email }
func (u *User) Role() Role                 { return u.state.Role }
func (u *User) PasswordHash() PasswordHash { return u.state.PasswordHash }
func (u *User) GoogleID() string           { return u.state.GoogleID }
func (u *User) IsEmailVerified() bool      { return u.state.EmailVerifiedAt != nil }
func (u *User) IsDeleted() bool            { return u.state.DeletedAt != nil }

func (u *User) DeletedAt() time.Time {
	if u.state.DeletedAt == nil {
		return time.Time{}
	}
	return *u.state.DeletedAt
}

// CanAuthenticateWithPassword membedakan "kata sandi salah" dari "akun ini
// memang tidak punya kata sandi". Keduanya menolak login, tetapi hanya yang
// kedua boleh menawarkan penetapan kata sandi.
func (u *User) CanAuthenticateWithPassword() bool { return u.state.PasswordHash != "" }

// LinkGoogle menautkan identitas Google ke akun yang sudah ada.
//
// Menutup S5. Metode ini DILARANG menyentuh PasswordHash, dan tidak ada jalan
// lain untuk menautkan Google - jadi kekeliruan sistem lama, yang menimpa
// kata sandi akun yang sudah ada dengan string acak setiap kali login sosial,
// tidak punya tempat untuk terjadi lagi.
func (u *User) LinkGoogle(googleID string, now time.Time) error {
	if strings.TrimSpace(googleID) == "" {
		return errors.New("empty google id")
	}
	if u.state.GoogleID != "" && u.state.GoogleID != googleID {
		return fmt.Errorf("%w: %q", ErrGoogleAlreadyLinked, u.state.GoogleID)
	}

	u.state.GoogleID = googleID
	// Google sudah membuktikan alamatnya; verifikasi yang tertunda selesai.
	if u.state.EmailVerifiedAt == nil {
		verified := now
		u.state.EmailVerifiedAt = &verified
	}
	u.state.UpdatedAt = now
	return nil
}

// SetPasswordHash dipakai reset kata sandi dan penetapan kata sandi pertama
// oleh pengguna yang selama ini hanya memakai Google.
func (u *User) SetPasswordHash(hash PasswordHash, now time.Time) error {
	if hash == "" {
		return ErrEmptyPasswordHash
	}
	u.state.PasswordHash = hash
	u.state.UpdatedAt = now
	return nil
}

// Delete menandai penghapusan lunak dan tidak menggeser waktu penghapusan
// yang sudah tercatat: penghapusan kedua adalah pengulangan permintaan, bukan
// peristiwa baru.
func (u *User) Delete(now time.Time) {
	if u.state.DeletedAt != nil {
		return
	}
	at := now
	u.state.DeletedAt = &at
	u.state.UpdatedAt = now
}
