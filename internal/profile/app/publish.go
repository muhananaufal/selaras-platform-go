package app

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

// EventWriter menulis event ke outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// EventWriterFor membuat penulis event DI ATAS satu transaksi.
//
// Pabrik, bukan penulis yang sudah jadi: penulis yang dibangun di atas kolam
// koneksi akan commit sendiri, dan eventnya bertahan meski perubahan profilnya
// batal - menyiarkan perubahan yang tidak pernah terjadi.
type EventWriterFor func(pg.Querier) EventWriter

// UnitOfWork menjalankan sebuah fungsi di dalam satu transaksi.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(pg.Querier) error) error
}

// ProfileRepositoryFor membuat repository di atas satu transaksi.
type ProfileRepositoryFor func(pg.Querier) domain.ProfileRepository

// WithEvents memasang penerbitan event pada service.
//
// Terpisah dari NewService supaya profile-svc tetap bisa berjalan tanpa broker,
// dan supaya test yang hanya menguji aturan domain tidak perlu menyediakannya.
// Yang TIDAK boleh terjadi adalah menyiarkan diam-diam sebagian: kalau salah
// satu dari ketiganya tidak ada, penerbitan dimatikan seluruhnya dan itu
// dinyatakan di log saat start.
func (s *Service) WithEvents(uow UnitOfWork, repos ProfileRepositoryFor, events EventWriterFor) *Service {
	if uow == nil || repos == nil || events == nil {
		return s
	}
	s.uow = uow
	s.repos = repos
	s.events = events
	return s
}

// PublishesEvents menyatakan apakah perubahan profil disiarkan.
func (s *Service) PublishesEvents() bool {
	return s.uow != nil && s.repos != nil && s.events != nil
}

// UpdateAndPublish menerapkan perubahan lalu menyiarkannya, dalam SATU
// transaksi.
//
// Ia terpisah dari Update, dan pemisahannya disengaja: Update dipakai jalur
// yang tidak perlu menyiarkan apa pun - pembuatan profil kosong saat mendaftar,
// misalnya - dan menggabungkan keduanya akan membuat setiap pemanggil harus
// memutuskan sesuatu yang bukan urusannya.
//
// Tanpa penerbitan yang terpasang, ia jatuh ke Update biasa. Itu bukan mode
// diam-diam: PublishesEvents menyatakannya, dan main mencatatnya saat start.
func (s *Service) UpdateAndPublish(
	ctx context.Context, userID domain.UserID, changes domain.ProfileChanges,
) (*domain.Profile, error) {
	if !s.PublishesEvents() {
		return s.Update(ctx, userID, changes)
	}

	var updated *domain.Profile
	err := s.uow.Do(ctx, func(q pg.Querier) error {
		// Service sementara di atas transaksi ini. Memakai s.profiles di sini
		// berarti perubahan profilnya commit sendiri, terpisah dari eventnya -
		// dan proses yang mati di antaranya meninggalkan profil yang berubah
		// tanpa ada yang tahu.
		txService := &Service{profiles: s.repos(q), now: s.now}

		profile, err := txService.Update(ctx, userID, changes)
		if err != nil {
			return err
		}
		updated = profile

		return s.events(q).Write(ctx, "user_profile", profile.UserID().String(), envelopeFor(profile))
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// envelopeFor menyusun event dari profil yang baru tersimpan.
func envelopeFor(p *domain.Profile) *eventsv1.Envelope {
	updated := &eventsv1.ProfileUpdated{
		UserProfileId: p.ID().String(),

		// user_id ikut, dan itu wajib: konsumennya mencari lewat identitas yang
		// terverifikasi di setiap permintaan, bukan lewat id profil.
		UserId:   p.UserID().String(),
		Sex:      string(p.Sex()),
		Language: string(p.Language()),
	}

	// Tanggal lahir dan negara boleh kosong (ADR-002 aturan 2), dan bedanya
	// nyata: nilai kosong yang dikirim akan tersimpan di cache sebagai
	// "diketahui kosong", sedangkan yang tidak dikirim berarti "belum diisi".
	if dob := p.DateOfBirth(); dob.IsStated() {
		iso := dob.String()
		updated.DateOfBirth = &iso
	}
	if country := p.CountryOfResidence(); country != "" {
		updated.CountryOfResidence = &country
	}

	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(p.UpdatedAt()),
		SchemaVersion: 1,
		Payload:       &eventsv1.Envelope_ProfileUpdated{ProfileUpdated: updated},
	}
}
