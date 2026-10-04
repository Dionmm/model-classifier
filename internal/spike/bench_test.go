package spike

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRunBenchBaselineRatioAndRetry(t *testing.T) {
	srv, calls := fakeJev(t)
	var slept []time.Duration
	var out bytes.Buffer
	spawns := []map[string]any{{"prompt": strings.Repeat("a", 400), "model": "opus"}, {"prompt": "retry"}}
	res := RunBench(context.Background(), spawns, BenchOptions{
		Client: &JevClient{Endpoint: srv.URL, APIKey: "k", AuthScheme: "Bearer",
			Model: "jev-test", Timeout: 5 * time.Second},
		Modes:       []string{"fresh", "reuse"},
		MaxAttempts: 3,
		Out:         &out,
		Sleep:       func(d time.Duration) { slept = append(slept, d) },
	})
	if len(res) != 5 || calls.Load() != 6 {
		t.Fatalf("results=%d calls=%d, want 5 results from 6 calls (one retry)", len(res), calls.Load())
	}
	if res[0].Mode != "baseline" || res[0].InputTokens != 100 {
		t.Fatalf("baseline: %+v", res[0])
	}
	r := res[1]
	if r.Status != 200 || r.CharsPerToken != 4 || r.BaselineTokens != 100 || r.JevModel != "jev-1.13.0" || r.OrchestratorModel != "opus" {
		t.Errorf("fresh result: %+v", r)
	}
	retried := slices.IndexFunc(res, func(r BenchResult) bool { return r.Attempts == 2 && r.Status == 200 })
	if retried < 0 {
		t.Errorf("429 was not retried to success: %+v", res)
	}
	if !slices.Contains(slept, time.Second) {
		t.Errorf("Retry-After not honoured; slept %v", slept)
	}
	if n := strings.Count(out.String(), "\n"); n != 5 {
		t.Errorf("wrote %d JSONL lines, want 5", n)
	}
	var rep bytes.Buffer
	WriteBenchReport(&rep, res)
	if !strings.Contains(rep.String(), "tier (placeholder criteria): deep=") {
		t.Errorf("report:\n%s", rep.String())
	}
}
