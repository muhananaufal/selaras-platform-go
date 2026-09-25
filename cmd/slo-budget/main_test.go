package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePrometheus answers /api/v1/query with body and records the query it
// was asked, so a test can check both the verdict and the question.
func fakePrometheus(t *testing.T, status int, body string) (*httptest.Server, *string) {
	t.Helper()
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("path = %q, want /api/v1/query", r.URL.Path)
		}
		asked = r.URL.Query().Get("query")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func vector(samples ...string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(samples, ",") + `]}}`
}

func sample(slo, value string) string {
	return `{"metric":{"sloth_service":"edge-gateway","sloth_slo":"` + slo + `"},"value":[1790000000.5,"` + value + `"]}`
}

func TestExitCodeFollowsTheRemainingBudget(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode int
		wantOut  []string
	}{
		{
			name:     "every budget left",
			status:   http.StatusOK,
			body:     vector(sample("requests-availability", "0.8"), sample("requests-latency", "0.4")),
			wantCode: exitOK,
			wantOut:  []string{"requests-availability", "80.0%", "requests-latency", "40.0%"},
		},
		{
			name:     "one budget overspent freezes releases",
			status:   http.StatusOK,
			body:     vector(sample("requests-availability", "0.8"), sample("requests-latency", "-0.25")),
			wantCode: exitExhausted,
			wantOut:  []string{"requests-latency", "-25.0%", "EXHAUSTED"},
		},
		{
			name:     "a budget at exactly zero is spent",
			status:   http.StatusOK,
			body:     vector(sample("requests-availability", "0")),
			wantCode: exitExhausted,
			wantOut:  []string{"EXHAUSTED"},
		},
		{
			name:     "no series means the SLO is not being measured",
			status:   http.StatusOK,
			body:     vector(),
			wantCode: exitUnknown,
			wantOut:  []string{"no SLO series"},
		},
		{
			name:     "NaN (no traffic in the period) is not a verdict",
			status:   http.StatusOK,
			body:     vector(sample("requests-availability", "0.9"), sample("requests-latency", "NaN")),
			wantCode: exitUnknown,
			wantOut:  []string{"requests-latency", "no data"},
		},
		{
			name:     "Prometheus error",
			status:   http.StatusBadRequest,
			body:     `{"status":"error","errorType":"bad_data","error":"parse error"}`,
			wantCode: exitUnknown,
			wantOut:  []string{"parse error"},
		},
		{
			name:     "not JSON",
			status:   http.StatusBadGateway,
			body:     `<html>bad gateway</html>`,
			wantCode: exitUnknown,
			wantOut:  []string{"502"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, asked := fakePrometheus(t, tc.status, tc.body)
			var out bytes.Buffer
			code := run([]string{"-prometheus", srv.URL, "-service", "edge-gateway"}, &out)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d; output:\n%s", code, tc.wantCode, out.String())
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
			want := `slo:period_error_budget_remaining:ratio{sloth_service="edge-gateway"}`
			if *asked != want {
				t.Errorf("query = %q, want %q", *asked, want)
			}
		})
	}
}

func TestUnreachablePrometheusIsUnknownNotGreen(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	var out bytes.Buffer
	if code := run([]string{"-prometheus", url}, &out); code != exitUnknown {
		t.Fatalf("exit code = %d, want %d; output:\n%s", code, exitUnknown, out.String())
	}
}

func TestBadFlagsAreUsageErrors(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"-no-such-flag"}, &out); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if code := run([]string{"-service", `x"} or vector(1) #`}, &out); code != exitUsage {
		t.Fatalf("a service name that escapes the label matcher must be refused, got %d", code)
	}
}
