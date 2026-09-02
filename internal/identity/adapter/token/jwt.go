// Package token menerbitkan dan memverifikasi token akses JWT.
package token

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// algorithm adalah EdDSA (Ed25519), dan itu asimetris dengan sengaja.
//
// Dengan HMAC, setiap unit yang perlu memverifikasi token juga memegang
// kunci untuk menerbitkannya - sembilan unit yang semuanya bisa mencetak
// token admin. Di sini hanya identity-svc yang memegang kunci privat, dan
// yang lain hanya bisa memeriksa.
//
// Ed25519 dipilih di antara yang asimetris karena kuncinya pendek, tanda
// tangannya cepat diverifikasi, dan tidak ada parameter yang bisa salah
// dipilih seperti pada RSA.
var algorithm = jwt.SigningMethodEdDSA

// claims memetakan domain.Claims ke bentuk JWT.
//
// Nama klaim standar dipakai bila ada padanannya - sub, iss, exp, iat, jti -
// supaya token bisa dibaca perkakas mana pun. Yang tidak punya padanan
// diberi awalan untuk menghindari tabrakan dengan klaim terdaftar kelak.
type claims struct {
	jwt.RegisteredClaims
	UserProfileID string `json:"upid,omitempty"`
	Email         string `json:"email,omitempty"`
	Role          string `json:"role"`
	Generation    int64  `json:"gen"`
}

// Issuer menandatangani klaim. Ia memegang kunci privat, jadi hanya
// identity-svc yang boleh membangunnya.
type Issuer struct {
	key      ed25519.PrivateKey
	issuer   string
	lifetime time.Duration
	now      func() time.Time
}

func NewIssuer(key ed25519.PrivateKey, issuerName string, lifetime time.Duration) (*Issuer, error) {
	// Ed25519 menerima slice berukuran apa pun tanpa mengeluh sampai saat
	// menandatangani, di mana ia panik. Ukurannya diperiksa di sini supaya
	// konfigurasi yang keliru menggagalkan start-up, bukan permintaan login
	// pertama.
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key is %d bytes; want %d", len(key), ed25519.PrivateKeySize)
	}
	if issuerName == "" {
		return nil, errors.New("empty issuer name")
	}
	if lifetime == 0 {
		return nil, errors.New("zero token lifetime")
	}
	return &Issuer{key: key, issuer: issuerName, lifetime: lifetime, now: time.Now}, nil
}

var _ domain.TokenIssuer = (*Issuer)(nil)

func (i *Issuer) Issue(c domain.Claims) (string, error) {
	// jti membuat dua token yang terbit pada detik yang sama untuk pengguna
	// yang sama tetap berbeda, sehingga log bisa membedakan sesi.
	jti, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generating token id: %w", err)
	}

	issuedAt := i.now()
	tok := jwt.NewWithClaims(algorithm, claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti.String(),
			Subject:   c.UserID.String(),
			Issuer:    i.issuer,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(issuedAt.Add(i.lifetime)),
		},
		UserProfileID: c.UserProfileID,
		Email:         c.Email,
		Role:          c.Role.String(),
		Generation:    c.Generation,
	})

	signed, err := tok.SignedString(i.key)
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}
	return signed, nil
}

// Verifier memeriksa tanda tangan dan masa berlaku. Ia hanya memegang kunci
// publik, jadi aman diberikan ke unit mana pun.
type Verifier struct {
	key    ed25519.PublicKey
	issuer string
	parser *jwt.Parser
}

func NewVerifier(key ed25519.PublicKey, issuerName string) (*Verifier, error) {
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes; want %d", len(key), ed25519.PublicKeySize)
	}
	if issuerName == "" {
		return nil, errors.New("empty issuer name")
	}

	return &Verifier{
		key:    key,
		issuer: issuerName,
		parser: jwt.NewParser(
			// Pertukaran algoritma hari ini sudah tertutup oleh tipe kunci:
			// verifikasi HMAC menuntut []byte telanjang, sedangkan keyfunc
			// mengembalikan ed25519.PublicKey yang bertipe bernama, dan
			// type assertion-nya gagal. Daftar ini adalah lapis kedua -
			// ia tetap menolak bila kelak keyfunc diubah mengembalikan
			// []byte, yang akan membuat kunci publik yang sengaja
			// disebarkan menjadi rahasia HMAC yang sah.
			jwt.WithValidMethods([]string{algorithm.Alg()}),
			jwt.WithIssuer(issuerName),
			// Token tanpa exp tidak pernah berhenti berlaku. Kadaluwarsa
			// diwajibkan, bukan sekadar diperiksa bila ada.
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
		),
	}, nil
}

var _ domain.TokenVerifier = (*Verifier)(nil)

func (v *Verifier) Verify(raw string) (domain.Claims, error) {
	var c claims

	// Setiap kegagalan dibungkus menjadi ErrInvalidToken yang sama, dan
	// error aslinya ikut dibungkus supaya log server tetap bisa
	// membedakan tanda tangan keliru dari kedaluwarsa.
	//
	// Yang DILARANG membedakannya adalah jawaban ke klien: memberi tahu
	// penyerang bahwa tanda tangannya benar tetapi sudah lewat berarti
	// memberi tahu bahwa kuncinya bocor. Penyeragaman itu dilakukan di
	// pemetaan error HTTP, bukan dengan membuang keterangan di sini.
	_, err := v.parser.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) {
		return v.key, nil
	})
	if err != nil {
		return domain.Claims{}, fmt.Errorf("%w: %w", domain.ErrInvalidToken, err)
	}

	userID, err := domain.ParseUserID(c.Subject)
	if err != nil {
		return domain.Claims{}, fmt.Errorf("%w: subject is not a user id", domain.ErrInvalidToken)
	}
	role, err := domain.NewRole(c.Role)
	if err != nil {
		return domain.Claims{}, fmt.Errorf("%w: unknown role %q", domain.ErrInvalidToken, c.Role)
	}
	// Generasi nol berarti klaimnya hilang. Menerimanya akan membuat token
	// tanpa generasi selamat dari setiap pencabutan.
	if c.Generation < 1 {
		return domain.Claims{}, fmt.Errorf("%w: missing token generation", domain.ErrInvalidToken)
	}
	if c.IssuedAt == nil || c.ExpiresAt == nil {
		return domain.Claims{}, fmt.Errorf("%w: missing iat or exp", domain.ErrInvalidToken)
	}

	return domain.Claims{
		UserID:        userID,
		UserProfileID: c.UserProfileID,
		Email:         c.Email,
		Role:          role,
		Generation:    c.Generation,
		IssuedAt:      c.IssuedAt.Time,
		ExpiresAt:     c.ExpiresAt.Time,
	}, nil
}
