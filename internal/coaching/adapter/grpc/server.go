package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// Server melayani coaching.v1.
type Server struct {
	coachingv1.UnimplementedCoachingServer
	svc *app.Service
}

func NewServer(svc *app.Service) (*Server, error) {
	if svc == nil {
		return nil, errors.New("nil coaching service")
	}
	return &Server{svc: svc}, nil
}

var _ coachingv1.CoachingServer = (*Server)(nil)

func (s *Server) StartProgram(
	ctx context.Context, req *coachingv1.StartProgramRequest,
) (*coachingv1.StartProgramResponse, error) {
	result, err := s.svc.StartProgram(ctx, app.StartProgramCommand{
		UserID:         req.GetUserId(),
		AssessmentSlug: req.GetRiskAssessmentSlug(),
		Difficulty:     difficultyFromProto(req.GetDifficulty()),
		IdempotencyKey: req.GetIdempotencyKey().GetValue(),
	})
	if err != nil {
		return nil, toStatus(ctx, "StartProgram", err)
	}

	return &coachingv1.StartProgramResponse{
		Program: programToProto(&app.ProgramView{Program: result.Program}),
		JobId:   result.Program.ID.String(),
	}, nil
}

func (s *Server) GetProgram(
	ctx context.Context, req *coachingv1.GetProgramRequest,
) (*coachingv1.GetProgramResponse, error) {
	view, err := s.svc.ShowProgram(ctx, req.GetSlug(), req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetProgram", err)
	}
	return &coachingv1.GetProgramResponse{Program: programToProto(view)}, nil
}

func (s *Server) ToggleProgramStatus(
	ctx context.Context, req *coachingv1.ToggleProgramStatusRequest,
) (*coachingv1.ToggleProgramStatusResponse, error) {
	program, err := s.svc.ToggleProgramStatus(ctx, req.GetSlug(), req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "ToggleProgramStatus", err)
	}
	return &coachingv1.ToggleProgramStatusResponse{
		Program: programToProto(&app.ProgramView{Program: program}),
	}, nil
}

func (s *Server) DeleteProgram(
	ctx context.Context, req *coachingv1.DeleteProgramRequest,
) (*coachingv1.DeleteProgramResponse, error) {
	if err := s.svc.DestroyProgram(ctx, req.GetSlug(), req.GetUserId()); err != nil {
		return nil, toStatus(ctx, "DeleteProgram", err)
	}
	return &coachingv1.DeleteProgramResponse{}, nil
}

func (s *Server) ToggleTaskStatus(
	ctx context.Context, req *coachingv1.ToggleTaskStatusRequest,
) (*coachingv1.ToggleTaskStatusResponse, error) {
	result, err := s.svc.ToggleTaskStatus(ctx, req.GetTaskId(), req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "ToggleTaskStatus", err)
	}
	return &coachingv1.ToggleTaskStatusResponse{Task: taskToProto(result.Task)}, nil
}

func (s *Server) GetGraduationReport(
	ctx context.Context, req *coachingv1.GetGraduationReportRequest,
) (*coachingv1.GetGraduationReportResponse, error) {
	view, err := s.svc.RequestGraduationReport(ctx, req.GetSlug(), req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetGraduationReport", err)
	}

	out := &coachingv1.GetGraduationReportResponse{
		Status: graduationToProto(view.Program.GraduationStatus),
	}
	if len(view.Report) > 0 {
		encoded, err := json.Marshal(view.Report)
		if err != nil {
			return nil, toStatus(ctx, "GetGraduationReport", err)
		}
		out.ReportJson = string(encoded)
	}
	return out, nil
}

func (s *Server) StartThread(
	ctx context.Context, req *coachingv1.StartThreadRequest,
) (*coachingv1.StartThreadResponse, error) {
	view, err := s.svc.StartNewThread(ctx, app.StartThreadCommand{
		ProgramSlug:    req.GetProgramSlug(),
		UserID:         req.GetUserId(),
		Title:          req.GetTitle(),
		FirstMessage:   req.GetMessage(),
		IdempotencyKey: req.GetIdempotencyKey().GetValue(),
	})
	if err != nil {
		return nil, toStatus(ctx, "StartThread", err)
	}

	return &coachingv1.StartThreadResponse{
		Thread: threadToProto(view.Thread),
		JobId:  view.Messages[0].ID.String(),
	}, nil
}

func (s *Server) GetThread(
	ctx context.Context, req *coachingv1.GetThreadRequest,
) (*coachingv1.GetThreadResponse, error) {
	view, err := s.svc.ShowThread(ctx, req.GetSlug(), req.GetUserId())
	if err != nil {
		return nil, toStatus(ctx, "GetThread", err)
	}

	messages := make([]*coachingv1.CoachingMessage, 0, len(view.Messages))
	for _, m := range view.Messages {
		messages = append(messages, messageToProto(m))
	}
	return &coachingv1.GetThreadResponse{
		Thread:   threadToProto(view.Thread),
		Messages: messages,
	}, nil
}

func (s *Server) UpdateThreadTitle(
	ctx context.Context, req *coachingv1.UpdateThreadTitleRequest,
) (*coachingv1.UpdateThreadTitleResponse, error) {
	thread, err := s.svc.RenameThread(ctx, req.GetSlug(), req.GetUserId(), req.GetTitle())
	if err != nil {
		return nil, toStatus(ctx, "UpdateThreadTitle", err)
	}
	return &coachingv1.UpdateThreadTitleResponse{Thread: threadToProto(thread)}, nil
}

func (s *Server) DeleteThread(
	ctx context.Context, req *coachingv1.DeleteThreadRequest,
) (*coachingv1.DeleteThreadResponse, error) {
	if err := s.svc.DestroyThread(ctx, req.GetSlug(), req.GetUserId()); err != nil {
		return nil, toStatus(ctx, "DeleteThread", err)
	}
	return &coachingv1.DeleteThreadResponse{}, nil
}

func (s *Server) SendThreadMessage(
	ctx context.Context, req *coachingv1.SendThreadMessageRequest,
) (*coachingv1.SendThreadMessageResponse, error) {
	message, err := s.svc.SendMessage(ctx, app.SendMessageCommand{
		ThreadSlug:     req.GetThreadSlug(),
		UserID:         req.GetUserId(),
		Text:           req.GetMessage(),
		IdempotencyKey: req.GetIdempotencyKey().GetValue(),
	})
	if err != nil {
		return nil, toStatus(ctx, "SendThreadMessage", err)
	}

	return &coachingv1.SendThreadMessageResponse{
		Message: messageToProto(message),
		JobId:   message.ID.String(),
	}, nil
}

// toStatus menerjemahkan galat domain menjadi kode gRPC.
//
// Yang TIDAK dikenali menjadi Internal dan dicatat lengkap. Menerjemahkannya
// menjadi InvalidArgument akan menyembunyikan kerusakan sebagai kesalahan
// pemanggil, dan tidak ada yang menyelidikinya.
func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case err == nil:
		return nil

	// Milik orang lain dan tidak ada menjawab SAMA (S9). Membedakannya memberi
	// tahu penanya bahwa slug itu ada.
	case errors.Is(err, domain.ErrProgramNotFound):
		return status.Error(codes.NotFound, "no such coaching program")
	case errors.Is(err, domain.ErrThreadNotFound):
		return status.Error(codes.NotFound, "no such thread")
	case errors.Is(err, domain.ErrTaskNotFound):
		return status.Error(codes.NotFound, "no such task")

	// Konflik keadaan: 409 di sisi HTTP.
	case errors.Is(err, domain.ErrActiveProgramExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, domain.ErrAssessmentUsed):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, domain.ErrProgramCompleted):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, domain.ErrProgramNotActive):
		return status.Error(codes.FailedPrecondition, err.Error())

	case errors.Is(err, domain.ErrInvalidDifficulty),
		errors.Is(err, domain.ErrInvalidStatus),
		errors.Is(err, domain.ErrInvalidRole),
		errors.Is(err, domain.ErrEmptyMessage),
		errors.Is(err, domain.ErrNoMessageAtAll),
		errors.Is(err, domain.ErrTitleTooLong),
		errors.Is(err, domain.ErrMessageTooLong),
		errors.Is(err, domain.ErrInvalidID):
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

// graduationToProto memetakan keadaan laporan kelulusan.
func graduationToProto(s domain.GraduationStatus) coachingv1.GraduationStatus {
	switch s {
	case domain.GraduationPending:
		return coachingv1.GraduationStatus_GRADUATION_STATUS_PENDING
	case domain.GraduationCompleted:
		return coachingv1.GraduationStatus_GRADUATION_STATUS_READY
	case domain.GraduationFailed:
		return coachingv1.GraduationStatus_GRADUATION_STATUS_FAILED
	default:
		return coachingv1.GraduationStatus_GRADUATION_STATUS_NOT_REQUESTED
	}
}
