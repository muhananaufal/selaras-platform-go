// Package authn membuat setiap service memverifikasi sendiri siapa yang
// meminta (ADR-026), alih-alih mempercayai user_id yang sekadar dikirimkan.
//
// Bentuknya: gateway meneruskan token akses pengguna sebagai metadata gRPC,
// service memverifikasi tanda tangannya dengan kunci publik yang sama yang
// dipegang gateway, lalu mencocokkan `sub` dengan `user_id` di permintaan.
// Tidak ada kunci privat di luar identity-svc, tidak ada rahasia bersama,
// tidak ada panggilan jaringan tambahan.
package authn

import (
	"context"
	"errors"
)

// Principal adalah yang terbukti dari sebuah token: siapa, dan generasi
// sesinya. Tidak lebih - peran dan surel tetap urusan identity-svc.
type Principal struct {
	UserID     string
	Generation int64
}

// ErrNoPrincipal dikembalikan PrincipalFrom saat permintaan tidak membawa
// token yang terverifikasi.
var ErrNoPrincipal = errors.New("no verified principal on this request")

type ctxKey int

const (
	keyToken ctxKey = iota
	keyPrincipal
)

// WithToken menaruh token akses mentah ke ctx supaya klien gRPC di hilir
// meneruskannya. Gateway memanggilnya setelah token diverifikasi; service
// tidak perlu, karena metadata masuk diteruskan otomatis oleh
// UnaryClientInterceptor.
func WithToken(ctx context.Context, raw string) context.Context {
	if raw == "" {
		return ctx
	}
	return context.WithValue(ctx, keyToken, raw)
}

// TokenFrom membaca token yang ditaruh WithToken.
func TokenFrom(ctx context.Context) (string, bool) {
	raw, ok := ctx.Value(keyToken).(string)
	return raw, ok && raw != ""
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, keyPrincipal, p)
}

// PrincipalFrom membaca identitas yang diverifikasi interceptor server.
//
// Handler yang ingin lebih dari sekadar "user_id cocok" - misalnya menolak
// generasi yang sudah dicabut - membacanya dari sini, bukan dari permintaan.
func PrincipalFrom(ctx context.Context) (Principal, error) {
	p, ok := ctx.Value(keyPrincipal).(Principal)
	if !ok {
		return Principal{}, ErrNoPrincipal
	}
	return p, nil
}
