package outbox_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// TestEveryEventInTheContractHasATopic reads the contract, not a hand-written
// list.
//
// Adding a new event to events.proto without giving it a topic would make the
// relay find its rows, fail to route them, and retry forever - a failure that
// surfaces long after the proto was changed. This test moves it to build time.
func TestEveryEventInTheContractHasATopic(t *testing.T) {
	fields := (&eventsv1.Envelope{}).ProtoReflect().Descriptor().Oneofs().ByName("payload").Fields()

	known := map[string]bool{}
	for _, topic := range kafka.Topics() {
		known[topic.Name] = true
	}

	var checked int
	for i := range fields.Len() {
		f := fields.Get(i)

		// The envelope is filled through its oneof field, and then its kind is
		// read through the same path the outbox writer uses.
		env := &eventsv1.Envelope{}
		env.ProtoReflect().Set(f, newMessageFor(env, f))

		eventType := outbox.EventTypeOf(env)
		if eventType == "" {
			t.Errorf("%s has no event type", f.Name())
			continue
		}

		topic, err := outbox.TopicFor(eventType)
		if err != nil {
			t.Errorf("%s (%s): %v", f.Name(), eventType, err)
			continue
		}
		if !known[topic] {
			t.Errorf("%s (%s) routes to topic %q, which no one creates", f.Name(), eventType, topic)
			continue
		}

		checked++
	}

	if checked == 0 {
		t.Fatal("no events were checked; the reflection walk found nothing")
	}
	t.Logf("%d events routed to topics that exist", checked)
}

func newMessageFor(env *eventsv1.Envelope, f protoreflect.FieldDescriptor) protoreflect.Value {
	return env.ProtoReflect().NewField(f)
}
