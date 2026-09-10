package llmworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

// Metrics are the three numbers that answer "is the queue healthy" (F3-15).
//
// Three, not thirty. A metric nobody will look at when something is wrong
// only adds to what has to be filtered out, and what remains here is what
// actually changes an action: how far behind, how long one job takes, and how
// many fail.
type Metrics struct {
	// duration measures how long one job takes, from claim to completion.
	//
	// A histogram, not an average: an average hides the tail, and the tail is
	// what makes the queue pile up. One job waiting five minutes holds its
	// partition for that long.
	duration metric.Float64Histogram

	// outcomes counts jobs by their result.
	//
	// One counter with an attribute, not three separate counters: the number
	// of successes and failures has to be comparable without summing different
	// series.
	outcomes metric.Int64Counter

	// tokens counts the tokens the provider REPORTS, by kind (input, output,
	// thinking), provider, and template. This is the number FinOps
	// (docs/finops.md) has been waiting for since F9: before this, the cost
	// per job was estimated from the template size divided by four.
	tokens metric.Int64Counter
}

// Token kinds for the counter attribute.
const (
	TokensInput    = "input"
	TokensOutput   = "output"
	TokensThoughts = "thoughts"
)

// Outcome is the attribute value for the outcome counter.
const (
	OutcomeCompleted = "completed"
	OutcomeFailed    = "failed"
	OutcomeDead      = "dead"
	OutcomeSkipped   = "skipped"
	OutcomeAbandoned = "abandoned"
	OutcomeParked    = "parked"
)

// NewMetrics membuat instrumennya.
func NewMetrics(meter metric.Meter) (*Metrics, error) {
	if meter == nil {
		return nil, errors.New("nil meter")
	}

	duration, err := meter.Float64Histogram("llm_job_duration_seconds",
		metric.WithDescription("How long one LLM job took, from claim to outcome"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, fmt.Errorf("building the duration histogram: %w", err)
	}

	outcomes, err := meter.Int64Counter("llm_jobs_total",
		metric.WithDescription("LLM jobs by outcome"))
	if err != nil {
		return nil, fmt.Errorf("building the outcome counter: %w", err)
	}

	tokens, err := meter.Int64Counter("llm_tokens_total",
		metric.WithDescription("Tokens reported by the LLM provider, by kind, provider, and template"))
	if err != nil {
		return nil, fmt.Errorf("building the token counter: %w", err)
	}

	return &Metrics{duration: duration, outcomes: outcomes, tokens: tokens}, nil
}

// Observe records one finished job.
func (m *Metrics) Observe(ctx context.Context, outcome string, took time.Duration) {
	if m == nil {
		// A worker may run without metrics. It records less, but does not behave
		// differently - and a nil check here beats every caller having to
		// remember it.
		return
	}

	attrs := metric.WithAttributes(attribute.String("outcome", outcome))
	m.outcomes.Add(ctx, 1, attrs)
	m.duration.Record(ctx, took.Seconds(), attrs)
}

// ObserveUsage records the tokens of one answer.
//
// Zero is not recorded: the fake provider reports no tokens, and a zero-valued
// series for it would read as if the job were free.
func (m *Metrics) ObserveUsage(ctx context.Context, provider, template string, u llm.Usage) {
	if m == nil || u.Total() == 0 {
		return
	}
	for kind, n := range map[string]int{
		TokensInput: u.InputTokens, TokensOutput: u.OutputTokens, TokensThoughts: u.ThoughtsTokens,
	} {
		if n == 0 {
			continue
		}
		m.tokens.Add(ctx, int64(n), metric.WithAttributes(
			attribute.String("kind", kind),
			attribute.String("provider", provider),
			attribute.String("template", template),
		))
	}
}

// LagReporter reports consumer lag periodically.
//
// Lag CANNOT be computed from the consumer's side alone: it needs the
// latest offset on the broker, and that is an admin question. That is why
// it is fetched through kadm, not from the consumer client.
type LagReporter struct {
	admin *kadm.Client
	group string
	gauge metric.Int64ObservableGauge
}

// NewLagReporter registers the lag measurement on the meter.
//
// It is an observable gauge, not a pushed value: lag changes constantly,
// and pushing it means choosing a rhythm of our own that need not match the
// reader's. An observable gauge is measured when asked.
func NewLagReporter(meter metric.Meter, client *kgo.Client, group string) (*LagReporter, error) {
	switch {
	case meter == nil:
		return nil, errors.New("nil meter")
	case client == nil:
		return nil, errors.New("nil kafka client")
	case group == "":
		return nil, errors.New("lag has no meaning without a consumer group")
	}

	gauge, err := meter.Int64ObservableGauge("kafka_consumer_lag",
		metric.WithDescription("How many records this consumer group is behind, per partition"))
	if err != nil {
		return nil, fmt.Errorf("building the lag gauge: %w", err)
	}

	r := &LagReporter{admin: kadm.NewClient(client), group: group, gauge: gauge}

	if _, err := meter.RegisterCallback(r.observe, gauge); err != nil {
		return nil, fmt.Errorf("registering the lag callback: %w", err)
	}
	return r, nil
}

// observe reads the lag when the metric is requested.
func (r *LagReporter) observe(ctx context.Context, o metric.Observer) error {
	// Its own deadline: a metrics read must not hang because of a slow broker.
	// A metrics page that never answers is as bad as no metrics at all.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	lags, err := r.admin.Lag(ctx, r.group)
	if err != nil {
		return fmt.Errorf("reading consumer lag: %w", err)
	}

	described, ok := lags[r.group]
	if !ok {
		// A group that has never existed is not an error - a freshly started
		// worker has not joined yet. Reporting it as an error would make the
		// metrics page fail during the first few seconds of every start.
		return nil
	}

	for topic, partitions := range described.Lag {
		for partition, memberLag := range partitions {
			if memberLag.Err != nil || memberLag.Lag < 0 {
				// A lag of -1 means the offset could not be read
				// [kadm@v1.18.0/groups.go:1412]. Reporting it as a number would show -1
				// on a graph as if it were a measurement.
				continue
			}
			o.ObserveInt64(r.gauge, memberLag.Lag, metric.WithAttributes(
				attribute.String("topic", topic),
				attribute.Int("partition", int(partition)),
			))
		}
	}
	return nil
}
