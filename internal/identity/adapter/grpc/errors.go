// Package grpc melayani kontrak identity.v1 di atas gRPC.
package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// toStatus menerjemahkan galat domain menjadi status gRPC.
//
// Pemetaannya terkumpul di satu tempat dengan sengaja. Tersebar di setiap
// handler, akan selalu ada satu handler yang lupa - dan yang lupa itu
// mengembalikan galat internal apa adanya ke pemanggil.
//
// Galat yang tidak dikenali TIDAK PERNAH dikirim isinya. Ia dicatat lengkap
// di sisi server dan dijawab dengan satu kalimat tetap: pesan galat internal
// membawa nama tabel, potongan kueri, dan alamat host, dan semuanya berguna
// bagi orang yang sedang memetakan sistem ini.
func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case err == nil:
		return nil

	// Kredensial salah selalu Unauthenticated, tanpa keterangan tambahan.
	// Membedakan "email tidak terdaftar" dari "kata sandi keliru" di sini
	// akan membatalkan penyeragaman yang dikerjakan use case-nya.
	case errors.Is(err, app.ErrInvalidCredentials):
		return status.Error(codes.Unauthenticated, "invalid credentials")

	case errors.Is(err, domain.ErrEmailTaken):
		return status.Error(codes.AlreadyExists, "that email address is already registered")

	case errors.Is(err, domain.ErrGoogleIDTaken), errors.Is(err, domain.ErrGoogleAlreadyLinked):
		return status.Error(codes.FailedPrecondition, "that social identity is linked to another account")

	case errors.Is(err, app.ErrEmailNotVerifiedByProvider):
		return status.Error(codes.PermissionDenied, "the provider has not verified this email address")

	case errors.Is(err, app.ErrUnsupportedProvider):
		return status.Error(codes.InvalidArgument, "unsupported social provider")

	// Token reset yang tidak sah selalu satu jawaban. "Token ini pernah ada
	// tetapi sudah dipakai" memberi tahu penyerang bahwa tebakannya benar.
	case errors.Is(err, domain.ErrResetTokenInvalid):
		return status.Error(codes.InvalidArgument, "invalid or expired reset token")

	case errors.Is(err, app.ErrPasswordMismatch):
		return status.Error(codes.InvalidArgument, "password confirmation does not match")

	// Galat validasi boleh disampaikan apa adanya: isinya memang tentang
	// masukan si pemanggil sendiri, dan menyembunyikannya hanya membuat
	// klien menebak-nebak apa yang salah.
	case errors.Is(err, domain.ErrInvalidEmail),
		errors.Is(err, domain.ErrPasswordTooShort),
		errors.Is(err, domain.ErrPasswordTooLong),
		errors.Is(err, domain.ErrInvalidRole),
		errors.Is(err, domain.ErrInvalidUserID):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, domain.ErrUserNotFound):
		return status.Error(codes.NotFound, "no such account")

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the caller went away")

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the deadline passed")

	default:
		slog.ErrorContext(ctx, "unhandled error", "operation", op, "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}
