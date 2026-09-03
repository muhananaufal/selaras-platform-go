package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
)

func ptr[T any](v T) *T { return &v }

func userID(t *testing.T) domain.UserID {
	t.Helper()

	id, err := domain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseUserID: %v", err)
	}
	return id
}

// TestTheFirstAssessmentHasNoTrend memisahkan dua keadaan yang sistem lama
// campur menjadi satu.
//
// Sistem lama menjawab "stable" untuk analisis pertama, dengan teks penjelas
// yang menerangkan bahwa itu sebenarnya bukan stabil. Klien yang menggambar
// panah mendatar untuk "stabil" akan menggambarnya juga untuk orang yang belum
// punya pembanding sama sekali.
func TestTheFirstAssessmentHasNoTrend(t *testing.T) {
	if got := domain.TrendBetween(12.5, nil); got != domain.TrendInsufficientData {
		t.Errorf("with no previous assessment the trend is %q", got)
	}
	if got := domain.ChangeBetween(12.5, nil); got != 0 {
		t.Errorf("with no previous assessment the change is %v", got)
	}

	empty := &domain.Dashboard{UserID: userID(t)}
	if got := empty.Trend(); got != domain.TrendInsufficientData {
		t.Errorf("an empty dashboard reports trend %q", got)
	}
	if !empty.IsEmpty() {
		t.Error("a dashboard with no assessments is not reported as empty")
	}
}

// TestTheTrendIgnoresChangesTooSmallToMean anything menjaga deadband-nya.
//
// Tanpa deadband, kabar tentang risiko kesehatan berubah arah setiap kali
// seseorang mengisi ulang kuesioner, dan kabar seperti itu berhenti dipercaya.
func TestTheTrendIgnoresChangesTooSmallToMean(t *testing.T) {
	for _, tc := range []struct {
		name             string
		latest, previous float64
		wantTrend        domain.Trend
		wantChange       float64
	}{
		{"identical", 12.5, 12.5, domain.TrendStable, 0},
		{"just inside the deadband, downwards", 12.4, 12.5, domain.TrendStable, 0},
		{"just inside the deadband, upwards", 12.6, 12.5, domain.TrendStable, 0},
		{"just outside, downwards", 12.35, 12.5, domain.TrendImproving, -0.15},
		{"just outside, upwards", 12.65, 12.5, domain.TrendWorsening, 0.15},
		{"clearly better", 8, 20, domain.TrendImproving, -12},
		{"clearly worse", 20, 8, domain.TrendWorsening, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.TrendBetween(tc.latest, &tc.previous); got != tc.wantTrend {
				t.Errorf("from %.2f to %.2f the trend is %q, want %q",
					tc.previous, tc.latest, got, tc.wantTrend)
			}
			if got := domain.ChangeBetween(tc.latest, &tc.previous); got != tc.wantChange {
				t.Errorf("from %.2f to %.2f the change is %v, want %v",
					tc.previous, tc.latest, got, tc.wantChange)
			}
		})
	}
}

// TestTheChangeIsRoundedLikeTheOldSystem menjaga angka yang dilihat pengguna.
func TestTheChangeIsRoundedLikeTheOldSystem(t *testing.T) {
	previous := 10.0
	if got := domain.ChangeBetween(12.3456, &previous); got != 2.35 {
		t.Errorf("a change of 2.3456 is reported as %v, want 2.35", got)
	}
}

// TestTheRiskTrendHoldsThirtyDaysOldestFirst adalah grafiknya.
func TestTheRiskTrendHoldsThirtyDaysOldestFirst(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	// Riwayat disimpan TERBARU lebih dulu, sebagaimana dibaca dari basis data.
	dash := &domain.Dashboard{
		UserID: userID(t),
		Total:  4,
		History: []*domain.Assessment{
			{Slug: "d", AssessedAt: now.AddDate(0, 0, -1), RiskPercentage: 11},
			{Slug: "c", AssessedAt: now.AddDate(0, 0, -10), RiskPercentage: 12},
			{Slug: "b", AssessedAt: now.AddDate(0, 0, -29), RiskPercentage: 13},
			// Di luar jendela: 31 hari lalu.
			{Slug: "a", AssessedAt: now.AddDate(0, 0, -31), RiskPercentage: 14},
		},
	}

	points := dash.RiskTrend(now)
	if len(points) != 3 {
		t.Fatalf("the graph holds %d points, want 3 - the 31-day-old one is outside the window", len(points))
	}

	// TERLAMA lebih dulu: grafik dibaca kiri ke kanan sebagai waktu yang maju.
	for i, want := range []string{"b", "c", "d"} {
		if points[i].Slug != want {
			t.Errorf("point %d is %q, want %q", i, points[i].Slug, want)
		}
	}

	// Slice kosong, bukan nil.
	if got := (&domain.Dashboard{}).RiskTrend(now); got == nil {
		t.Error("an empty graph is nil, which becomes null in JSON")
	}
}

// TestADashboardWithHistoryIsNotEmpty menjaga kedua penanda tetap sepakat.
func TestADashboardWithHistoryIsNotEmpty(t *testing.T) {
	dash := &domain.Dashboard{
		UserID:   userID(t),
		Total:    2,
		Latest:   &domain.Assessment{Slug: "b", RiskPercentage: 9},
		Previous: ptr(20.0),
	}

	if dash.IsEmpty() {
		t.Error("a dashboard with two assessments is reported as empty")
	}
	if got := dash.Trend(); got != domain.TrendImproving {
		t.Errorf("a drop from 20 to 9 is reported as %q", got)
	}
	if got := dash.Change(); got != -11 {
		t.Errorf("a drop from 20 to 9 is reported as %v", got)
	}
}
