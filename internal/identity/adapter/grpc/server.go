package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/app"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// SocialIdentityVerifier mengubah ID token dari penyedia menjadi identitas
// yang sudah diverifikasi.
//
// Ia ada di sini, bukan di app, karena ia berbicara dengan penyedia -
// mengambil JWKS dan memeriksa tanda tangan. Yang sampai ke use case hanyalah
// hasilnya (ADR-021 koreksi 3).
type SocialIdentityVerifier interface {
	Verify(ctx context.Context, provider, idToken string) (app.SocialIdentity, error)
}

// UseCases mengumpulkan alur yang dilayani server ini.
//
// Ia diserahkan sebagai satu struct, bukan sebagai daftar argumen, supaya
// menambah satu alur tidak mengubah tanda tangan konstruktornya - dan
// pemanggil yang lupa mengisi salah satunya ditangkap saat penyusunan, bukan
// saat permintaan pertama yang menyentuhnya.
type UseCases struct {
	Register       *app.Register
	Login          *app.Login
	Logout         *app.Logout
	RequestReset   *app.RequestPasswordReset
	ConfirmReset   *app.ConfirmPasswordReset
	ExchangeSocial *app.ExchangeSocialToken

	// Deletion boleh nil: lingkungan tanpa outbox tetap melayani autentikasi.
	// Penghapusan akun menjawab Unimplemented alih-alih menghapus sebagian.
	Deletion *app.DeleteAccount
	Users    domain.UserRepository

	// Tokens dipakai Logout untuk memverifikasi tanda tangan token yang
	// dikirim balik. Ia BUKAN untuk memverifikasi setiap permintaan - itu
	// pekerjaan gateway dengan kunci publiknya sendiri (ADR-021 koreksi 1).
	Tokens domain.TokenVerifier
	Social SocialIdentityVerifier

	// AccessTokenTTL diumumkan ke klien lewat expires_in_seconds.
	//
	// Ia ikut di sini, bukan dibaca ulang dari penerbit token, supaya angka
	// yang diberitahukan ke klien dan angka yang benar-benar dipakai berasal
	// dari satu sumber. Dua sumber berarti suatu saat keduanya berbeda, dan
	// klien akan memperbarui token pada waktu yang keliru.
	AccessTokenTTLSeconds int64
}

// Server melayani identity.v1.
type Server struct {
	identityv1.UnimplementedIdentityServer
	uc UseCases
}

func NewServer(uc UseCases) (*Server, error) {
	switch {
	case uc.Register == nil:
		return nil, errors.New("nil register use case")
	case uc.Login == nil:
		return nil, errors.New("nil login use case")
	case uc.Logout == nil:
		return nil, errors.New("nil logout use case")
	case uc.RequestReset == nil:
		return nil, errors.New("nil password reset request use case")
	case uc.ConfirmReset == nil:
		return nil, errors.New("nil password reset confirm use case")
	case uc.ExchangeSocial == nil:
		return nil, errors.New("nil social exchange use case")
	case uc.Users == nil:
		return nil, errors.New("nil user repository")
	case uc.Tokens == nil:
		return nil, errors.New("nil token verifier")
	case uc.Social == nil:
		return nil, errors.New("nil social identity verifier")
	case uc.AccessTokenTTLSeconds <= 0:
		return nil, errors.New("access token lifetime must be positive")
	}
	return &Server{uc: uc}, nil
}

var _ identityv1.IdentityServer = (*Server)(nil)

func (s *Server) Register(
	ctx context.Context,
	req *identityv1.RegisterRequest,
) (*identityv1.RegisterResponse, error) {
	result, err := s.uc.Register.Execute(ctx, app.RegisterCommand{
		Email: req.GetEmail(),
		// Konfirmasi kata sandi tidak ada di kontrak gRPC dan itu disengaja:
		// mengetik ulang kata sandi adalah pemeriksaan antarmuka pengguna,
		// dan tempatnya di edge yang memang menerimanya dari peramban.
		Password:             req.GetPassword(),
		PasswordConfirmation: req.GetPassword(),
	})
	if err != nil {
		return nil, toStatus(ctx, "Register", err)
	}

	user, err := s.loadUser(ctx, result.UserID)
	if err != nil {
		return nil, toStatus(ctx, "Register", err)
	}

	return &identityv1.RegisterResponse{
		User:     user,
		Token:    s.tokenPair(result.AccessToken),
		Identity: identityOf(result),
	}, nil
}

func (s *Server) Login(
	ctx context.Context,
	req *identityv1.LoginRequest,
) (*identityv1.LoginResponse, error) {
	result, err := s.uc.Login.Execute(ctx, app.LoginCommand{
		Email:    req.GetEmail(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, toStatus(ctx, "Login", err)
	}

	user, err := s.loadUser(ctx, result.UserID)
	if err != nil {
		return nil, toStatus(ctx, "Login", err)
	}

	return &identityv1.LoginResponse{
		User:     user,
		Token:    s.tokenPair(result.AccessToken),
		Identity: identityOf(result),
	}, nil
}

// Logout menerima access token, bukan user id.
//
// Tanda tangannya diverifikasi DI SINI, bukan dipercaya dari pemanggil.
// Gateway memang sudah memverifikasinya, tetapi identity-svc tidak boleh
// bergantung pada itu: kalau ia menerima user id yang sekadar dikirimkan,
// siapa pun yang bisa menjangkau service ini bisa mengeluarkan pengguna mana
// pun dari sesinya hanya dengan menebak id.
//
// identity-svc memegang kunci penandatanganan, jadi ia bisa memverifikasi
// sendiri tanpa meminta apa pun kepada siapa pun.
func (s *Server) Logout(
	ctx context.Context,
	req *identityv1.LogoutRequest,
) (*identityv1.LogoutResponse, error) {
	claims, err := s.uc.Tokens.Verify(req.GetAccessToken())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	if err := s.uc.Logout.Execute(ctx, claims.UserID); err != nil {
		return nil, toStatus(ctx, "Logout", err)
	}
	return &identityv1.LogoutResponse{}, nil
}

func (s *Server) RequestPasswordReset(
	ctx context.Context,
	req *identityv1.RequestPasswordResetRequest,
) (*identityv1.RequestPasswordResetResponse, error) {
	if err := s.uc.RequestReset.Execute(ctx, app.RequestPasswordResetCommand{
		Email: req.GetEmail(),
	}); err != nil {
		return nil, toStatus(ctx, "RequestPasswordReset", err)
	}
	return &identityv1.RequestPasswordResetResponse{}, nil
}

func (s *Server) ConfirmPasswordReset(
	ctx context.Context,
	req *identityv1.ConfirmPasswordResetRequest,
) (*identityv1.ConfirmPasswordResetResponse, error) {
	if err := s.uc.ConfirmReset.Execute(ctx, app.ConfirmPasswordResetCommand{
		Token:                req.GetToken(),
		Password:             req.GetNewPassword(),
		PasswordConfirmation: req.GetNewPassword(),
	}); err != nil {
		return nil, toStatus(ctx, "ConfirmPasswordReset", err)
	}
	return &identityv1.ConfirmPasswordResetResponse{}, nil
}

func (s *Server) ExchangeSocialToken(
	ctx context.Context,
	req *identityv1.ExchangeSocialTokenRequest,
) (*identityv1.ExchangeSocialTokenResponse, error) {
	// Tanda tangan ID token diperiksa di sini, bukan dipercaya dari
	// pemanggil. Klaim email_verified adalah tumpuan pengerasan di F1-11,
	// dan ia hanya berarti selama tanda tangan penyedianya masih utuh.
	identity, err := s.uc.Social.Verify(ctx, req.GetProvider(), req.GetIdToken())
	if err != nil {
		return nil, toStatus(ctx, "ExchangeSocialToken", err)
	}

	result, err := s.uc.ExchangeSocial.Execute(ctx, identity)
	if err != nil {
		return nil, toStatus(ctx, "ExchangeSocialToken", err)
	}

	user, err := s.loadUser(ctx, result.UserID)
	if err != nil {
		return nil, toStatus(ctx, "ExchangeSocialToken", err)
	}

	return &identityv1.ExchangeSocialTokenResponse{
		User:     user,
		Token:    s.tokenPair(result.AccessToken),
		Identity: identityOf(result),
		// Profil yang baru dibuat hanya terjadi pada akun baru, jadi
		// ketiadaannya menandakan akun yang sudah ada baru saja ditautkan.
		AccountWasLinked: result.UserProfileID == "",
	}, nil
}

func (s *Server) DeleteAccount(
	ctx context.Context,
	req *identityv1.DeleteAccountRequest,
) (*identityv1.DeleteAccountResponse, error) {
	if s.uc.Deletion == nil {
		// Tanpa outbox, saga tidak bisa diumumkan. Ia menjawab Unimplemented
		// alih-alih menghapus sebagian: penghapusan yang berhenti di tengah
		// meninggalkan data di unit yang tidak dituju siapa pun lagi.
		return nil, status.Error(codes.Unimplemented,
			"account deletion needs the outbox, which is not configured here")
	}

	saga, err := s.uc.Deletion.Execute(ctx, app.DeleteAccountCommand{
		UserID:   req.GetUserId(),
		Password: req.GetPassword(),
	})
	if err != nil {
		return nil, deletionStatus(ctx, err)
	}
	return &identityv1.DeleteAccountResponse{SagaId: saga.ID.String()}, nil
}

// GetTokenGeneration menjawab generasi token yang sedang berlaku.
//
// Gateway memanggilnya HANYA saat cache pencabutan tidak tahu, bukan di
// setiap request (ADR-021 koreksi 1).
func (s *Server) GetTokenGeneration(
	ctx context.Context,
	req *identityv1.GetTokenGenerationRequest,
) (*identityv1.GetTokenGenerationResponse, error) {
	userID, err := domain.ParseUserID(req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetTokenGeneration", err)
	}

	user, err := s.uc.Users.FindByID(ctx, userID)
	if err != nil {
		return nil, toStatus(ctx, "GetTokenGeneration", err)
	}

	return &identityv1.GetTokenGenerationResponse{
		Generation: user.TokenGeneration(),
	}, nil
}

func (s *Server) loadUser(ctx context.Context, id string) (*identityv1.User, error) {
	userID, err := domain.ParseUserID(id)
	if err != nil {
		return nil, err
	}
	user, err := s.uc.Users.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	return &identityv1.User{
		Id:            user.ID().String(),
		Email:         user.Email().String(),
		Role:          roleOf(user.Role()),
		EmailVerified: user.IsEmailVerified(),
	}, nil
}

func (s *Server) tokenPair(accessToken string) *identityv1.TokenPair {
	return &identityv1.TokenPair{
		AccessToken:      accessToken,
		ExpiresInSeconds: s.uc.AccessTokenTTLSeconds,
	}
}

func identityOf(result app.AuthResult) *commonv1.Identity {
	return &commonv1.Identity{
		UserId:        result.UserID,
		UserProfileId: result.UserProfileID,
	}
}

// roleOf memetakan peran domain ke enum kontrak.
//
// Peran yang tidak dikenal menjadi ROLE_UNSPECIFIED, bukan ROLE_USER. Nilai
// nol enum protobuf memang berarti "tidak dinyatakan", dan memetakannya ke
// peran nyata akan membuat data yang rusak terlihat seperti pengguna biasa.
func roleOf(r domain.Role) identityv1.Role {
	switch r {
	case domain.RoleUser:
		return identityv1.Role_ROLE_USER
	case domain.RoleAdmin:
		return identityv1.Role_ROLE_ADMIN
	default:
		return identityv1.Role_ROLE_UNSPECIFIED
	}
}

// deletionStatus menerjemahkan galat penghapusan akun.
//
// Kata sandi yang keliru menjawab PermissionDenied, bukan Unauthenticated:
// pemanggilnya sudah terautentikasi, dan Unauthenticated akan membuat gateway
// serta klien mengira tokennya kedaluwarsa lalu memintanya masuk lagi - untuk
// kesalahan yang sebenarnya hanya salah ketik.
func deletionStatus(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, app.ErrWrongPassword):
		return status.Error(codes.PermissionDenied, "the password does not match")

	case errors.Is(err, app.ErrDeletionInProgress):
		return status.Error(codes.FailedPrecondition,
			"this account is already being deleted")

	case errors.Is(err, domain.ErrUserNotFound):
		return status.Error(codes.NotFound, "no such account")

	case errors.Is(err, domain.ErrInvalidUserID):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the caller went away")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the deadline passed")

	default:
		slog.ErrorContext(ctx, "unhandled error", "operation", "DeleteAccount", "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}
