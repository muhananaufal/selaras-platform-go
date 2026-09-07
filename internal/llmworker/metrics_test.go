package llmworker

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

// collect membaca seluruh titik data llm_tokens_total sebagai peta
// "kind|provider|template" -> nilai.
func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "llm_tokens_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("llm_tokens_total is %T, want an int64 sum", m.Data)
			}
			for _, dp := range sum.DataPoints {
				kind, _ := dp.Attributes.Value(attribute.Key("kind"))
				provider, _ := dp.Attributes.Value(attribute.Key("provider"))
				template, _ := dp.Attributes.Value(attribute.Key("template"))
				out[kind.AsString()+"|"+provider.AsString()+"|"+template.AsString()] = dp.Value
			}
		}
	}
	return out
}

func TestTokenUsageIsCountedByKind(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	m, err := NewMetrics(meter)
	if err != nil {
		t.Fatal(err)
	}

	m.ObserveUsage(context.Background(), "gemini", "curriculum@1",
		llm.Usage{InputTokens: 900, OutputTokens: 1500, ThoughtsTokens: 300})
	m.ObserveUsage(context.Background(), "gemini", "curriculum@1",
		llm.Usage{InputTokens: 100, OutputTokens: 0, ThoughtsTokens: 0})

	got := collect(t, reader)
	want := map[string]int64{
		"input|gemini|curriculum@1":    1000,
		"output|gemini|curriculum@1":   1500,
		"thoughts|gemini|curriculum@1": 300,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
}

// Penyedia yang tidak melaporkan token tidak boleh menghasilkan deret nol.
func TestUnreportedUsageLeavesNoSeries(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	m, err := NewMetrics(meter)
	if err != nil {
		t.Fatal(err)
	}
	m.ObserveUsage(context.Background(), "fake", "chat_reply@1", llm.Usage{})
	if got := collect(t, reader); len(got) != 0 {
		t.Fatalf("a zero usage produced series: %v", got)
	}
	var nilMetrics *Metrics
	nilMetrics.ObserveUsage(context.Background(), "fake", "x", llm.Usage{InputTokens: 1})
}
