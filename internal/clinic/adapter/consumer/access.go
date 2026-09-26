// Package consumer reads the events clinic-svc stores (ADR-030).
package consumer

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
)

// AccessStore stores one access in a patient's audit, idempotently by event.
type AccessStore interface {
	RecordAccess(ctx context.Context, a domain.Access) error
}

// AccessRecords reads clinic.access - clinicians' reads recorded by the
// services that served them - into the patients' append-only access audit.
type AccessRecords struct {
	client *kgo.Client
	store  AccessStore
	log    *slog.Logger
}

func NewAccessRecords(client *kgo.Client, store AccessStore, log *slog.Logger) (*AccessRecords, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case store == nil:
		return nil, errors.New("nil access store")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &AccessRecords{client: client, store: store, log: log}, nil
}

// Run reads until ctx is done.
func (a *AccessRecords) Run(ctx context.Context) error {
	return loop(ctx, a.client, a.log, "access audit", a.handle)
}

// handle stores one recorded read.
//
// A record that can never be stored - not an envelope, not this event, a
// resource the audit has no name for, ids that are not ids - is logged and
// dropped: holding its offset would stop every audit behind it, forever.
// Anything else that fails is returned, so the offset is held and the record
// comes back; storing is idempotent by the envelope's event id.
func (a *AccessRecords) handle(ctx context.Context, rec *kgo.Record) error {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		a.log.ErrorContext(ctx, "an access record could not be decoded and was dropped",
			"offset", rec.Offset, "error", err)
		return nil
	}
	read := env.GetClinicianAccessRecorded()
	if read == nil {
		a.log.WarnContext(ctx, "an event that is not an access record arrived on clinic.access and was skipped",
			"event_id", env.GetEventId())
		return nil
	}

	resource, err := domain.ParseResource(read.GetResource())
	if err != nil {
		a.log.ErrorContext(ctx, "an access record names a resource the audit does not know and was dropped",
			"event_id", env.GetEventId(), "resource", read.GetResource(), "error", err)
		return nil
	}
	for _, id := range []string{env.GetEventId(), read.GetClinicianUserId(), read.GetPatientUserId()} {
		if _, err := uuid.Parse(id); err != nil {
			a.log.ErrorContext(ctx, "an access record carries an id that is not one and was dropped",
				"event_id", env.GetEventId(), "id", id, "error", err)
			return nil
		}
	}

	return a.store.RecordAccess(ctx, domain.Access{
		EventID:         env.GetEventId(),
		ClinicianUserID: read.GetClinicianUserId(),
		PatientUserID:   read.GetPatientUserId(),
		Resource:        resource,
		AccessedAt:      read.GetAccessedAt().AsTime(),
	})
}
