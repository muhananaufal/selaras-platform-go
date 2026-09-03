package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
)

// Server melayani assessment.v1.
type Server struct {
	assessmentv1.UnimplementedAssessmentServer
	svc       *app.Service
	constants score.Constants

	// uow dan events boleh nil: assessment-svc tetap melayani pembacaan
	// tanpa outbox. Yang tidak boleh adalah berpura-pura menerima pekerjaan
	// yang tidak akan pernah ada yang mengerjakan.
	uow    app.UnitOfWork
	events app.EventWriterFor
}

func NewServer(
	svc *app.Service,
	constants score.Constants,
	uow app.UnitOfWork,
	events app.EventWriterFor,
) (*Server, error) {
	if svc == nil {
		return nil, errors.New("nil assessment service")
	}
	return &Server{svc: svc, constants: constants, uow: uow, events: events}, nil
}

var _ assessmentv1.AssessmentServer = (*Server)(nil)

func (s *Server) StartAssessment(
	ctx context.Context,
	req *assessmentv1.StartAssessmentRequest,
) (*assessmentv1.StartAssessmentResponse, error) {
	if req.GetInput() == nil {
		return nil, status.Error(codes.InvalidArgument, "no assessment input was sent")
	}

	assessment, err := s.svc.Start(ctx, app.StartCommand{
		UserID:  req.GetUserId(),
		Answers: AnswersFrom(req.GetInput()),
	})
	if err != nil {
		return nil, toStatus(ctx, "StartAssessment", err)
	}

	return &assessmentv1.StartAssessmentResponse{
		Assessment: toProto(assessment, req.GetInput()),
	}, nil
}

func (s *Server) GetAssessment(
	ctx context.Context,
	req *assessmentv1.GetAssessmentRequest,
) (*assessmentv1.GetAssessmentResponse, error) {
	assessment, err := s.svc.Get(ctx, req.GetSlug(), req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetAssessment", err)
	}
	return &assessmentv1.GetAssessmentResponse{Assessment: toProto(assessment, nil)}, nil
}

func (s *Server) ListAssessments(
	ctx context.Context,
	req *assessmentv1.ListAssessmentsRequest,
) (*assessmentv1.ListAssessmentsResponse, error) {
	found, err := s.svc.History(ctx, req.GetUserId(), int(req.GetPage().GetPageSize()))
	if err != nil {
		return nil, toStatus(ctx, "ListAssessments", err)
	}

	out := make([]*assessmentv1.RiskAssessment, 0, len(found))
	for _, a := range found {
		out = append(out, toProto(a, nil))
	}
	return &assessmentv1.ListAssessmentsResponse{Assessments: out}, nil
}

// ResolveRiskRegion memetakan negara ke wilayah kalibrasi.
//
// Ia murni: tidak menyentuh basis data dan tidak punya keadaan. Itulah
// sebabnya ia boleh dipanggil dari jalur baca profil tanpa membebani apa pun.
func (s *Server) ResolveRiskRegion(
	_ context.Context,
	req *assessmentv1.ResolveRiskRegionRequest,
) (*assessmentv1.ResolveRiskRegionResponse, error) {
	if req.GetCountryOfResidence() == "" {
		// Negara kosong TIDAK dipetakan ke "high" di sini.
		//
		// Nilai bawaan itu ada untuk negara yang tidak dikenali tabel, bukan
		// untuk profil yang belum diisi. Mengembalikan "high" untuk yang
		// kedua akan menampilkan wilayah risiko kepada pengguna yang belum
		// memberi tahu di mana ia tinggal.
		return nil, status.Error(codes.InvalidArgument, "no country of residence was sent")
	}
	return &assessmentv1.ResolveRiskRegionResponse{
		RiskRegion: s.constants.RegionFor(req.GetCountryOfResidence()),
	}, nil
}

// RequestPersonalization menerima permintaan lalu kembali segera.
//
// Ia TIDAK memanggil penyedia LLM. Yang terjadi hanyalah satu baris outbox,
// dan llm-worker yang mengerjakannya. Memanggil penyedia dari sini berarti
// pengguna menunggu puluhan detik dan satu kegagalan penyedia menjadi
// kegagalan HTTP yang tidak bisa dicoba ulang siapa pun.
func (s *Server) RequestPersonalization(
	ctx context.Context,
	req *assessmentv1.RequestPersonalizationRequest,
) (*assessmentv1.RequestPersonalizationResponse, error) {
	if s.uow == nil || s.events == nil {
		return nil, status.Error(codes.Unimplemented,
			"this service was started without an outbox and cannot queue work")
	}

	ticket, err := s.svc.RequestPersonalization(ctx, s.uow, s.events, app.PersonalizationRequest{
		Slug:           req.GetSlug(),
		UserID:         req.GetUserId(),
		IdempotencyKey: req.GetIdempotencyKey().GetValue(),
	})
	if err != nil {
		return nil, toStatus(ctx, "RequestPersonalization", err)
	}

	// PENDING, bukan COMPLETED, meski permintaannya berhasil diterima.
	// Perbedaannya yang memberi tahu klien bahwa masih ada yang perlu ditunggu.
	statusOut := assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_PENDING
	if ticket.AlreadyRunning {
		statusOut = assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED
	}

	return &assessmentv1.RequestPersonalizationResponse{
		JobId:  ticket.JobID,
		Status: statusOut,
	}, nil
}

// toProto memetakan penilaian ke bentuk kontrak.
//
// input hanya tersedia pada jalur Start, karena yang tersimpan adalah jawaban
// mentah dalam bentuk map - bukan pesan bertipe. Menyusun ulang pesan itu dari
// map akan menebak-nebak enum yang sudah tidak ada asalnya, jadi ia dibiarkan
// kosong dan cuplikan mentahnya yang menjadi catatan.
func toProto(a *domain.Assessment, input *assessmentv1.AssessmentInput) *assessmentv1.RiskAssessment {
	out := &assessmentv1.RiskAssessment{
		Id:             a.ID.String(),
		Slug:           a.Slug,
		UserProfileId:  a.UserProfileID.String(),
		ModelUsed:      modelToProto(a.ModelUsed),
		RiskPercentage: a.RiskPercentage,
		Input:          input,
		ResolvedValues: resolvedFrom(a.GeneratedValues),
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestamppb.New(a.CreatedAt),
			UpdatedAt: timestamppb.New(a.UpdatedAt),
		},
		PersonalizationStatus: personalizationStatus(a),
	}

	if a.ResultDetails != nil {
		if encoded, err := json.Marshal(a.ResultDetails); err == nil {
			report := string(encoded)
			out.PersonalizedReportJson = &report
		}
	}
	return out
}

// personalizationStatus dibaca dari kolomnya, bukan diturunkan (F3-12).
//
// Yang diturunkan dari ada tidaknya laporan hanya bisa membedakan dua keadaan,
// dan keduanya menyembunyikan yang paling perlu diketahui klien: pekerjaannya
// gagal, dan menunggu lebih lama tidak akan mengubah apa pun.
func personalizationStatus(a *domain.Assessment) assessmentv1.PersonalizationStatus {
	switch a.PersonalizationStatus {
	case domain.PersonalizationPending:
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_PENDING
	case domain.PersonalizationCompleted:
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED
	case domain.PersonalizationFailed:
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_FAILED
	case domain.PersonalizationNotRequested:
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_NOT_REQUESTED
	default:
		// Kolom kosong berarti baris yang ditulis sebelum kolomnya ada, atau
		// nilai yang tidak dikenali. Laporan yang ADA tetap dilaporkan selesai:
		// menyatakan "belum diminta" untuk laporan yang bisa dibaca klien akan
		// menawarkan tombol untuk pekerjaan yang sudah ada hasilnya.
		if a.ResultDetails != nil {
			return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_COMPLETED
		}
		return assessmentv1.PersonalizationStatus_PERSONALIZATION_STATUS_NOT_REQUESTED
	}
}

func modelToProto(name string) assessmentv1.RiskModel {
	switch name {
	case "SCORE2":
		return assessmentv1.RiskModel_RISK_MODEL_SCORE2
	case "SCORE2-OP":
		return assessmentv1.RiskModel_RISK_MODEL_SCORE2_OP
	case "SCORE2-Diabetes":
		return assessmentv1.RiskModel_RISK_MODEL_SCORE2_DIABETES
	default:
		// Nama model yang tidak dikenal menjadi UNSPECIFIED, bukan SCORE2.
		// Memetakannya ke model nyata akan membuat baris yang rusak terlihat
		// seperti penilaian biasa.
		return assessmentv1.RiskModel_RISK_MODEL_UNSPECIFIED
	}
}

// resolvedFrom membaca cuplikan nilai klinis yang tersimpan.
func resolvedFrom(values map[string]any) *assessmentv1.ResolvedClinicalValues {
	if values == nil {
		return nil
	}

	out := &assessmentv1.ResolvedClinicalValues{
		Age:                   int32(numberOf(values, "age")),
		Sex:                   stringOf(values, "sex_label"),
		RiskRegion:            stringOf(values, "determined_risk_region"),
		SystolicBloodPressure: numberOf(values, "sbp"),
		TotalCholesterol:      numberOf(values, "tchol"),
		HdlCholesterol:        numberOf(values, "hdl"),
	}

	// Ketiganya optional di kontrak, dan hanya ada pada jalur diabetes.
	// Mengirimkannya sebagai nol untuk penilaian lain akan membuat klien
	// menampilkan HbA1c nol - angka yang mustahil dan tampak seperti data.
	if v, ok := values["hba1c"]; ok {
		hba1c := toFloat(v)
		out.Hba1C = &hba1c
	}
	if v, ok := values["scr"]; ok {
		scr := toFloat(v)
		out.SerumCreatinine = &scr

		// eGFR tidak disimpan; ia diturunkan. Menghitungnya kembali dari
		// nilai yang tersimpan lebih jujur daripada menyimpan angka yang bisa
		// menyimpang dari rumusnya.
		egfr := score.EGFR(scr, int(numberOf(values, "age")), stringOf(values, "sex_label"))
		out.Egfr = &egfr
	}

	return out
}

func stringOf(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func numberOf(m map[string]any, key string) float64 {
	return toFloat(m[key])
}

// toFloat menerima kedua bentuk yang mungkin.
//
// Nilai yang baru dihitung bertipe int atau float64; yang dibaca kembali dari
// JSONB selalu float64. Menangani hanya satu di antaranya membuat cuplikan
// yang baru dan yang tersimpan berperilaku berbeda.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case err == nil:
		return nil

	// Milik orang lain dan tidak ada menjawab sama. Membedakannya memberi
	// tahu penanya bahwa slug itu ada.
	case errors.Is(err, domain.ErrAssessmentNotFound):
		return status.Error(codes.NotFound, "no such assessment")

	case errors.Is(err, app.ErrProfileIncomplete):
		return status.Error(codes.FailedPrecondition, err.Error())

	case errors.Is(err, domain.ErrInvalidProfileID), errors.Is(err, domain.ErrInvalidID):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, score.ErrUnknownSex), errors.Is(err, score.ErrMissingDiabetesInput):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the caller went away")

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the deadline passed")

	default:
		slog.ErrorContext(ctx, "unhandled error", "operation", op, "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}
