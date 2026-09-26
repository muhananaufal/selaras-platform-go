// Command slo-budget is the release-freeze gate of the error budget policy
// (docs/runbook/error-budget.md).
//
// It asks Prometheus how much of each SLO's 30-day error budget is left -
// the slo:period_error_budget_remaining:ratio series Sloth generates from
// deploy/slo/selaras.yml - and answers with an exit code a pipeline can act
// on:
//
//	0  every budget has something left: releases go out as usual
//	1  at least one budget is spent: feature releases stop, fixes still ship
//	2  no verdict: Prometheus unreachable, no series, or no traffic (NaN)
//	3  usage error
//
// "No verdict" is kept apart from both answers on purpose. Treating it as
// green would let an unmeasured system ship as if it were healthy; treating
// it as red would freeze releases every time Prometheus restarts. The caller
// decides, and the policy says how.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	exitOK        = 0
	exitExhausted = 1
	exitUnknown   = 2
	exitUsage     = 3
)

// serviceName limits -service to what Sloth accepts as a service name, so
// the value can be placed inside a PromQL label matcher without escaping.
var serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// budget is one SLO's remaining error budget as a ratio: 1 is untouched,
// 0 is spent, below 0 is overspent.
type budget struct {
	slo       string
	remaining float64
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("slo-budget", flag.ContinueOnError)
	fs.SetOutput(out)
	prometheus := fs.String("prometheus", "http://127.0.0.1:19090", "Prometheus base URL")
	service := fs.String("service", "edge-gateway", "sloth_service whose SLOs are checked")
	timeout := fs.Duration("timeout", 10*time.Second, "how long to wait for Prometheus")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	code, lines := decide(*prometheus, *service, *timeout)
	// A gate that cannot say why it decided is not trusted to say "go".
	if _, err := io.WriteString(out, strings.Join(lines, "\n")+"\n"); err != nil && code == exitOK {
		return exitUnknown
	}
	return code
}

func decide(prometheus, service string, timeout time.Duration) (int, []string) {
	if !serviceName.MatchString(service) {
		return exitUsage, []string{fmt.Sprintf("invalid -service %q: lowercase letters, digits and dashes only", service)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	budgets, err := query(ctx, http.DefaultClient, prometheus, service)
	if err != nil {
		return exitUnknown, []string{"NO VERDICT: " + err.Error()}
	}
	return judge(service, budgets)
}

// judge lists every SLO and picks the exit code. An overspent budget wins
// over a missing one: a known freeze is a verdict even when another SLO has
// no data.
func judge(service string, budgets []budget) (int, []string) {
	if len(budgets) == 0 {
		return exitUnknown, []string{fmt.Sprintf("NO VERDICT: no SLO series for service %q (are the Sloth rules loaded?)", service)}
	}
	sort.Slice(budgets, func(i, j int) bool { return budgets[i].slo < budgets[j].slo })

	var lines []string
	exhausted, unknown := false, false
	for _, b := range budgets {
		switch {
		case math.IsNaN(b.remaining) || math.IsInf(b.remaining, 0):
			unknown = true
			lines = append(lines, fmt.Sprintf("%-28s no data (no traffic in the period)", b.slo))
		case b.remaining <= 0:
			exhausted = true
			lines = append(lines, fmt.Sprintf("%-28s %6.1f%% EXHAUSTED", b.slo, b.remaining*100))
		default:
			lines = append(lines, fmt.Sprintf("%-28s %6.1f%% left", b.slo, b.remaining*100))
		}
	}
	switch {
	case exhausted:
		return exitExhausted, append(lines, "error budget spent: feature releases are frozen (docs/runbook/error-budget.md)")
	case unknown:
		return exitUnknown, append(lines, "NO VERDICT: at least one SLO has no data")
	default:
		return exitOK, lines
	}
}

// promResponse is the part of the /api/v1/query answer this command reads.
type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			// Value is [unix seconds, "value as a string"].
			Value [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func query(ctx context.Context, client *http.Client, base, service string) (budgets []budget, err error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + "/api/v1/query")
	if err != nil {
		return nil, fmt.Errorf("prometheus url: %w", err)
	}
	q := url.Values{}
	q.Set("query", fmt.Sprintf(`slo:period_error_budget_remaining:ratio{sloth_service=%q}`, service))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query prometheus: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close prometheus answer: %w", cerr)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read prometheus answer: %w", err)
	}
	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("prometheus answered HTTP %d with a body that is not its API: %w", resp.StatusCode, err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus answered HTTP %d: %s", resp.StatusCode, pr.Error)
	}
	if pr.Data.ResultType != "vector" {
		return nil, fmt.Errorf("expected an instant vector, got %q", pr.Data.ResultType)
	}

	budgets = make([]budget, 0, len(pr.Data.Result))
	for _, s := range pr.Data.Result {
		var raw string
		if err := json.Unmarshal(s.Value[1], &raw); err != nil {
			return nil, fmt.Errorf("sample value of %q: %w", s.Metric["sloth_slo"], err)
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("sample value of %q: %w", s.Metric["sloth_slo"], err)
		}
		slo := s.Metric["sloth_slo"]
		if slo == "" {
			return nil, errors.New("series without a sloth_slo label")
		}
		budgets = append(budgets, budget{slo: slo, remaining: v})
	}
	return budgets, nil
}
