package consumer

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	clinicpg "github.com/muhananaufal/selaras-platform-go/internal/clinic/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func newConsumer(t *testing.T) (*AccessRecords, *clinicpg.Repository, context.Context) {
	t.Helper()
	repo := clinicpg.NewRepository(pgtest.Open(t, "clinic"))
	client, err := kgo.NewClient(kgo.SeedBrokers("127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	c, err := NewAccessRecords(client, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c, repo, ctx
}

func accessRecord(t *testing.T, eventID, clinician, patient, resource string, at time.Time) *kgo.Record {
	t.Helper()
	env := &eventsv1.Envelope{
		EventId:       eventID,
		OccurredAt:    timestamppb.New(at),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_ClinicianAccessRecorded{ClinicianAccessRecorded: &eventsv1.ClinicianAccessRecorded{
			ClinicianUserId: clinician, PatientUserId: patient, Resource: resource, AccessedAt: timestamppb.New(at),
		}},
	}
	value, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{Topic: outbox.TopicClinicAccess, Key: []byte(patient), Value: value}
}

func auditOf(t *testing.T, ctx context.Context, repo *clinicpg.Repository, patient string) int {
	t.Helper()
	page, _, err := repo.AccessAudit(ctx, patient, 100, nil)
	if err != nil {
		t.Fatalf("AccessAudit: %v", err)
	}
	return len(page)
}

// A read recorded by another service lands in the patient's audit - once,
// however many times the relay delivers it.
func TestARecordedReadLandsInThePatientsAuditOnce(t *testing.T) {
	c, repo, ctx := newConsumer(t)
	patient, doc := uuid.NewString(), uuid.NewString()
	rec := accessRecord(t, uuid.NewString(), doc, patient, "risk_assessments", time.Now())

	for range 2 {
		if err := c.handle(ctx, rec); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}
	page, _, err := repo.AccessAudit(ctx, patient, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ClinicianUserID != doc || page[0].Resource != "risk_assessments" {
		t.Fatalf("the patient's audit is %+v; want the one read, once", page)
	}
}

// A record that can never be stored is dropped, not retried: holding its
// offset would stop every audit behind it, forever.
func TestARecordThatCanNeverBeStoredIsDropped(t *testing.T) {
	c, repo, ctx := newConsumer(t)
	patient := uuid.NewString()
	for name, rec := range map[string]*kgo.Record{
		"not protobuf":         {Topic: outbox.TopicClinicAccess, Value: []byte("not an envelope")},
		"a resource for chat":  accessRecord(t, uuid.NewString(), uuid.NewString(), patient, "chat", time.Now()),
		"a malformed patient":  accessRecord(t, uuid.NewString(), uuid.NewString(), "x", "risk_assessments", time.Now()),
		"a malformed event id": accessRecord(t, "x", uuid.NewString(), patient, "risk_assessments", time.Now()),
		"another event on it":  {Topic: outbox.TopicClinicAccess, Value: mustMarshal(t, &eventsv1.Envelope{EventId: uuid.NewString()})},
	} {
		if err := c.handle(ctx, rec); err != nil {
			t.Errorf("%s: handle returned %v; want it dropped", name, err)
		}
	}
	if n := auditOf(t, ctx, repo, patient); n != 0 {
		t.Fatalf("%d unusable records reached the audit", n)
	}
}

// A failure that may heal is still an error, so the offset is held and the
// record comes back.
func TestATransientFailureIsStillAnError(t *testing.T) {
	c, _, ctx := newConsumer(t)
	gone, cancel := context.WithCancel(ctx)
	cancel()
	rec := accessRecord(t, uuid.NewString(), uuid.NewString(), uuid.NewString(), "coaching_progress", time.Now())
	if err := c.handle(gone, rec); err == nil {
		t.Fatal("a store under a cancelled context did not fail")
	}
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
