package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// ErrWrongPassword menolak penghapusan yang kata sandinya tidak cocok.
//
// Ia dibedakan dari ErrInvalidCredentials karena pemanggilnya SUDAH terautentikasi:
// tidak ada akun yang bisa dicacah dari jawaban ini, dan pesan yang kabur hanya
// membuat orang mengira aplikasinya rusak saat mereka salah ketik.
var ErrWrongPassword = errors.New("the password does not match")

// ErrDeletionInProgress menolak permintaan kedua.
var ErrDeletionInProgress = errors.New("a deletion is already running for this account")

// SagaRepository menyimpan saga penghapusan.
type SagaRepository interface {
	Create(ctx context.Context, s *domain.DeletionSaga) error
	Find(ctx context.Context, id domain.SagaID) (*domain.DeletionSaga, error)

	// FindOutstandingForUser mengembalikan ErrSagaNotFound bila pengguna itu
	// tidak sedang dihapus.
	FindOutstandingForUser(ctx context.Context, userID domain.UserID) (*domain.DeletionSaga, error)

	// Confirm mencatat jawaban satu unit.
	//
	// IDEMPOTEN: jawaban yang sama dua kali menyisakan satu baris.
	Confirm(ctx context.Context, id domain.SagaID, c domain.Confirmation) error

	// Close menutup saga dengan keadaan akhirnya.
	Close(ctx context.Context, id domain.SagaID, status domain.SagaStatus, at time.Time) error

	// Outstanding menyebutkan saga yang belum selesai, terlama lebih dulu.
	// Dipakai runbook dan perintah verifikasi.
	Outstanding(ctx context.Context, limit int) ([]*domain.DeletionSaga, error)
}

// DeleteAccount memulai dan menyelesaikan saga penghapusan akun.
//
// Ia dipisahkan dari use case lain karena dependensinya berbeda: ia satu-satunya
// yang membutuhkan penyimpanan saga sekaligus pembanding kata sandi.
type DeleteAccount struct {
	users    domain.UserRepository
	sagas    SagaRepository
	hasher   domain.PasswordHasher
	profiles ProfileFinder
	uow      UnitOfWork
	now      func() time.Time
	log      *slog.Logger
}

func NewDeleteAccount(
	users domain.UserRepository,
	sagas SagaRepository,
	hasher domain.PasswordHasher,
	profiles ProfileFinder,
	uow UnitOfWork,
	now func() time.Time,
	log *slog.Logger,
) (*DeleteAccount, error) {
	switch {
	case users == nil:
		return nil, errors.New("nil user repository")
	case sagas == nil:
		return nil, errors.New("nil saga repository")
	case hasher == nil:
		return nil, errors.New("nil password hasher")
	case profiles == nil:
		return nil, errors.New("nil profile finder")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case now == nil:
		return nil, errors.New("nil clock")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &DeleteAccount{
		users: users, sagas: sagas, hasher: hasher,
		profiles: profiles, uow: uow, now: now, log: log,
	}, nil
}

// DeleteAccountCommand adalah permintaan penghapusan.
type DeleteAccountCommand struct {
	// UserID datang dari token yang sudah diverifikasi, bukan dari badan
	// permintaan (ADR-023).
	UserID string

	// Password DIVERIFIKASI, bukan sekadar diwajibkan ada.
	//
	// Sistem lama mewajibkannya di aturan validasi lalu tidak pernah
	// membandingkannya (temuan S2): siapa pun yang memegang token sah bisa
	// menghapus akun secara permanen dengan mengirim string apa pun.
	Password string
}

// Execute memulai saga penghapusan akun (F8-01).
//
// Ia TIDAK menghapus apa pun sendiri. Yang dilakukannya: memastikan orangnya
// benar, mencatat sagalnya, dan mengumumkan permintaannya. Enam unit menghapus
// datanya masing-masing lalu mengonfirmasi, dan akun barunya dihapus setelah
// keenamnya menjawab.
//
// Urutannya sengaja begitu. Menghapus akun lebih dulu akan menghilangkan
// satu-satunya tempat yang tahu penghapusan itu sedang berjalan, dan unit yang
// gagal menghapus datanya tidak punya siapa pun untuk dilapori.
func (d *DeleteAccount) Execute(
	ctx context.Context, cmd DeleteAccountCommand,
) (*domain.DeletionSaga, error) {
	userID, err := domain.ParseUserID(cmd.UserID)
	if err != nil {
		return nil, err
	}

	user, err := d.users.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Kata sandi diperiksa SEBELUM apa pun yang lain.
	//
	// Akun yang masuk lewat penyedia sosial tidak punya kata sandi. Ia tidak
	// bisa membuktikan dirinya dengan cara ini, dan menerima penghapusan tanpa
	// bukti apa pun justru mengembalikan lubang yang sedang ditutup - jadi
	// jalur itu DITOLAK dengan pesan yang menyebutkan sebabnya, bukan
	// diloloskan.
	if user.PasswordHash() == "" {
		return nil, fmt.Errorf(
			"%w: this account signs in through a provider and has no password to confirm with",
			ErrWrongPassword)
	}

	candidate, err := domain.NewPassword(cmd.Password)
	if err != nil {
		// Kata sandi yang tidak memenuhi bentuk minimal pun TIDAK boleh
		// melewatkan verifikasi; ia gagal di sini dengan jawaban yang sama.
		return nil, ErrWrongPassword
	}

	matches, _, err := d.hasher.Verify(user.PasswordHash(), candidate)
	if err != nil {
		return nil, fmt.Errorf("verifying the password: %w", err)
	}
	if !matches {
		return nil, ErrWrongPassword
	}

	// Permintaan kedua ditolak, bukan memulai saga kedua. Dua rangkaian
	// konfirmasi untuk satu akun akan membuat yang kedua mengira dirinya belum
	// lengkap - unit-unitnya sudah menjawab yang pertama.
	switch _, err := d.sagas.FindOutstandingForUser(ctx, userID); {
	case err == nil:
		return nil, ErrDeletionInProgress
	case !errors.Is(err, domain.ErrSagaNotFound):
		return nil, err
	}

	// Id profil disalin SEKARANG, saat profilnya masih ada.
	//
	// Beberapa unit menyimpan datanya dengan kunci itu, bukan dengan user_id.
	// Setelah profile-svc menghapus barisnya, tidak ada lagi yang bisa
	// menerjemahkannya - dan unit yang belum sempat menghapus kehilangan
	// satu-satunya cara menemukan data yang harus dihapusnya.
	//
	// Kegagalan mencarinya TIDAK menghentikan penghapusan: profil yang belum
	// pernah dibuat adalah keadaan yang sah (B7), dan menolak menghapus akun
	// karena profilnya tidak ada akan menjebak orang di akun yang tidak bisa
	// mereka tinggalkan.
	profileID, err := d.profiles.FindProfileID(ctx, userID)
	if err != nil {
		d.log.WarnContext(ctx, "could not resolve the profile id before deletion; the saga continues without it",
			"user_id", userID.String(), "error", err)
		profileID = ""
	}

	now := d.now()
	saga, err := domain.NewDeletionSaga(userID, profileID, now)
	if err != nil {
		return nil, err
	}

	// Saga dan eventnya ditulis dalam SATU transaksi (E10). Kalau keduanya bisa
	// terpisah, sistem bisa punya saga yang tidak pernah diumumkan - menggantung
	// selamanya menunggu enam unit yang tidak pernah diberi tahu - atau
	// pengumuman tanpa saga, yang menghapus data pengguna tanpa satu pun catatan
	// bahwa itu diminta.
	if err := d.uow.Do(ctx, func(r Repositories) error {
		if err := r.Sagas().Create(ctx, saga); err != nil {
			return err
		}
		return r.Events().Write(ctx, "user", userID.String(), deletionRequested(saga, now))
	}); err != nil {
		return nil, fmt.Errorf("starting the deletion saga: %w", err)
	}

	d.log.InfoContext(ctx, "an account deletion saga started",
		"saga_id", saga.ID.String(), "awaiting", saga.Outstanding())

	return saga, nil
}

// ConfirmDeletion mencatat jawaban satu unit dan menutup saga bila sudah lengkap.
func (d *DeleteAccount) ConfirmDeletion(
	ctx context.Context, sagaID string, c domain.Confirmation,
) error {
	id, err := domain.ParseSagaID(sagaID)
	if err != nil {
		return err
	}

	now := d.now()

	return d.uow.Do(ctx, func(r Repositories) error {
		saga, err := r.Sagas().Find(ctx, id)
		if err != nil {
			return err
		}

		// Jawaban yang tiba setelah saga ditutup bukan kegagalan: relay
		// at-least-once, dan yang kedua tiba setelah yang pertama menutupnya.
		status, err := saga.Confirm(c)
		if errors.Is(err, domain.ErrSagaAlreadyClosed) {
			return nil
		}
		if err != nil {
			return err
		}

		if err := r.Sagas().Confirm(ctx, id, c); err != nil {
			return err
		}
		if status == domain.SagaRequested {
			return nil
		}

		if err := r.Sagas().Close(ctx, id, status, now); err != nil {
			return err
		}

		// Akun BARU dihapus setelah keenam unit mengonfirmasi berhasil.
		//
		// Saga yang gagal meninggalkan akunnya utuh - dan itu disengaja.
		// Menghapus akun sementara datanya masih ada di suatu unit berarti
		// tidak ada lagi yang bisa menemukan data itu: tidak ada user_id yang
		// hidup untuk mencarinya, dan tidak ada orang yang bisa memintanya.
		if status != domain.SagaCompleted {
			d.log.ErrorContext(ctx, "a deletion saga finished with failures; the account is kept",
				"saga_id", id.String(), "failures", saga.Failures())
			return nil
		}

		if err := r.Users().Delete(ctx, saga.UserID); err != nil {
			return fmt.Errorf("deleting the account after every unit confirmed: %w", err)
		}

		d.log.InfoContext(ctx, "an account was deleted after every unit confirmed",
			"saga_id", id.String())
		return nil
	})
}

// deletionRequested menyusun event yang mengumumkan permintaannya.
func deletionRequested(s *domain.DeletionSaga, now time.Time) *eventsv1.Envelope {
	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: 1,

		// Kunci idempotensi diturunkan dari sagalnya: satu saga, satu
		// pengumuman, selamanya.
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "user-deletion:" + s.ID.String()},

		Payload: &eventsv1.Envelope_UserDeletionRequested{
			UserDeletionRequested: &eventsv1.UserDeletionRequested{
				SagaId:        s.ID.String(),
				UserId:        s.UserID.String(),
				UserProfileId: s.UserProfileID,
			},
		},
	}
}

// LogOutstandingSagas mencatat saga yang menggantung.
//
// Dipanggil saat start-up. Saga yang menggantung dari proses sebelumnya tidak
// akan pernah menyelesaikan dirinya sendiri - unit-unitnya sudah dihubungi, dan
// yang belum menjawab tidak akan ditanya lagi - jadi satu-satunya cara ia
// terlihat adalah kalau seseorang diberi tahu.
func (d *DeleteAccount) LogOutstandingSagas(ctx context.Context, log *slog.Logger) {
	sagas, err := d.sagas.Outstanding(ctx, 50)
	if err != nil {
		log.ErrorContext(ctx, "could not read outstanding deletion sagas", "error", err)
		return
	}
	if len(sagas) == 0 {
		return
	}

	log.WarnContext(ctx, "there are unfinished account deletions; see docs/runbook/account-deletion.md",
		"count", len(sagas))
	for _, saga := range sagas {
		log.WarnContext(ctx, "an account deletion is unfinished",
			"saga_id", saga.ID.String(),
			"requested_at", saga.RequestedAt,
			"awaiting", saga.Outstanding(),
			"failures", len(saga.Failures()))
	}
}
