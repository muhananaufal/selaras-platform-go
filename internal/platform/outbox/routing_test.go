package outbox_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// TestEveryEventInTheContractHasATopic membaca kontraknya, bukan daftar tulisan
// tangan.
//
// Menambah event baru di events.proto tanpa memberinya topic akan membuat relay
// menemukan barisnya, gagal merutekannya, dan mencoba lagi selamanya - kegagalan
// yang muncul jauh setelah proto-nya diubah. Test ini memindahkannya ke waktu
// build.
func TestEveryEventInTheContractHasATopic(t *testing.T) {
	fields := (&eventsv1.Envelope{}).ProtoReflect().Descriptor().Oneofs().ByName("payload").Fields()

	known := map[string]bool{}
	for _, topic := range kafka.Topics() {
		known[topic.Name] = true
	}

	var checked int
	for i := range fields.Len() {
		f := fields.Get(i)

		// Envelope diisi lewat bidang oneof-nya, lalu jenisnya dibaca dengan
		// jalur yang sama yang dipakai penulis outbox.
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
