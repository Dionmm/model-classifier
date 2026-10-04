package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dionmm/model-classifier/internal/jev"
	"github.com/Dionmm/model-classifier/internal/payload"
	"github.com/Dionmm/model-classifier/internal/router"
	"github.com/Dionmm/model-classifier/internal/telemetry"
	"github.com/Dionmm/model-classifier/internal/wire"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Now().Truncate(time.Millisecond)} }
func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}
func (f *fakeClock) Add(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

type fakeCaller struct {
	mu      sync.Mutex
	calls   int
	status  int
	body    []byte
	result  *jev.CallResult
	err     error
	block   chan struct{}
	started chan struct{}
}

func (f *fakeCaller) Call(ctx context.Context, _ any, _ map[string]any) (*jev.CallResult, error) {
	f.mu.Lock()
	f.calls++
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return f.result, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	st := f.status
	if st == 0 {
		st = 200
	}
	body := f.body
	if body == nil {
		body = validJev("deep", 0.95)
	}
	return &jev.CallResult{Status: st, Body: body}, nil
}

func (f *fakeCaller) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func validJev(tier string, conf float64) []byte {
	probs := `{"fast":0.01,"balanced":0.02,"deep":0.95,"max":0.02}`
	if tier == "fast" {
		probs = `{"fast":0.95,"balanced":0.02,"deep":0.02,"max":0.01}`
	}
	return []byte(`{"model":"jev-1.13.0","answers":{"tier":{"type":"choice","choice":"` + tier + `","confidence":` + strconvFloat(conf) + `,"probabilities":` + probs + `}},"usage":{"input_tokens":12}}`)
}

func strconvFloat(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func claudePayload(model, prompt string) []byte {
	if model == "" {
		return []byte(`{"session_id":"s","tool_name":"Agent","tool_use_id":"tu","tool_input":{"subagent_type":"research","description":"desc","prompt":` + quote(prompt) + `}}`)
	}
	return []byte(`{"session_id":"s","tool_name":"Agent","tool_use_id":"tu","tool_input":{"subagent_type":"research","description":"desc","prompt":` + quote(prompt) + `,"model":` + quote(model) + `}}`)
}

func copilotPayload(model, prompt string, stringArgs bool) []byte {
	args := `{"agent_type":"research","description":"desc","prompt":` + quote(prompt)
	if model != "" {
		args += `,"model":` + quote(model)
	}
	args += `}`
	if stringArgs {
		return []byte(`{"sessionId":"s","toolName":"task","toolCallId":"tc","toolArgs":` + quote(args) + `}`)
	}
	return []byte(`{"sessionId":"s","toolName":"task","toolCallId":"tc","toolArgs":` + args + `}`)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func testServer(t *testing.T, caller *fakeCaller, clock *fakeClock) *Server {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Harnesses[payload.ShapeClaude] = router.HarnessConfig{
		Enabled:      true,
		InputAliases: router.DefaultConfig().Harnesses[payload.ShapeClaude].InputAliases,
		Output:       router.DefaultConfig().Harnesses[payload.ShapeClaude].Output,
		NoModel:      router.NoModelConfig{AgentTypes: []string{"research"}, Threshold: 0.9},
	}
	cfg.Harnesses[payload.ShapeCopilot] = router.HarnessConfig{
		Enabled:      true,
		InputAliases: router.DefaultConfig().Harnesses[payload.ShapeCopilot].InputAliases,
		Output:       router.DefaultConfig().Harnesses[payload.ShapeCopilot].Output,
		NoModel:      router.NoModelConfig{AgentTypes: []string{"research"}, Threshold: 0.9},
	}
	s, err := New(Options{Config: cfg, Caller: caller, Clock: clock.Now, Questions: map[string]any{"tier": map[string]any{"type": "choice", "criteria": map[string]any{"fast": "", "balanced": "", "deep": "", "max": ""}}}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func routeReq(t *testing.T, s *Server, harness string, body []byte, deadline time.Time) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, wire.PathRoute, bytes.NewReader(body))
	req.Header.Set(wire.HeaderProtocol, wire.ProtocolVersion)
	req.Header.Set(wire.HeaderHarness, harness)
	req.Header.Set(wire.HeaderDeadline, strconv.FormatInt(deadline.UnixMilli(), 10))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func TestDeadlineSkipsOrHonoursJev(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{block: make(chan struct{})}
	s := testServer(t, caller, clock)
	start := time.Now()
	rr := routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(120*time.Millisecond))
	if rr.Body.Len() != 0 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("deadline did not fail open quickly: body=%q elapsed=%s", rr.Body.String(), time.Since(start))
	}
	if caller.Calls() != 1 {
		t.Fatalf("caller calls = %d, want 1", caller.Calls())
	}
	caller2 := &fakeCaller{}
	s2 := testServer(t, caller2, clock)
	rr = routeReq(t, s2, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(-time.Second))
	if rr.Body.Len() != 0 || caller2.Calls() != 0 {
		t.Fatalf("past deadline body=%q calls=%d, want empty zero calls", rr.Body.String(), caller2.Calls())
	}
	req := httptest.NewRequest(http.MethodPost, wire.PathRoute, bytes.NewReader(claudePayload("haiku", "p")))
	req.Header.Set(wire.HeaderProtocol, wire.ProtocolVersion)
	req.Header.Set(wire.HeaderHarness, payload.ShapeClaude)
	rr = httptest.NewRecorder()
	s2.Handler().ServeHTTP(rr, req)
	if rr.Body.Len() != 0 || caller2.Calls() != 0 {
		t.Fatalf("missing deadline body=%q calls=%d, want empty zero calls", rr.Body.String(), caller2.Calls())
	}
}

func TestCircuitBreakerFailuresProbeAnd4xx(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{err: errors.New("down")}
	s := testServer(t, caller, clock)
	for i := 0; i < 3; i++ {
		routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	}
	if s.breaker.State() != "open" {
		t.Fatalf("breaker state = %s, want open after 3 transport failures", s.breaker.State())
	}
	before := caller.Calls()
	routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	if caller.Calls() != before {
		t.Fatalf("open breaker called Jev: before=%d after=%d", before, caller.Calls())
	}
	clock.Add(31 * time.Second)
	caller.err = nil
	routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	if s.breaker.State() != "closed" {
		t.Fatalf("breaker state = %s, want closed after successful probe", s.breaker.State())
	}
	caller.status = 404
	for i := 0; i < 4; i++ {
		routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	}
	if s.breaker.State() != "closed" {
		t.Fatalf("4xx tripped breaker, state=%s", s.breaker.State())
	}
	caller.status = 500
	for i := 0; i < 3; i++ {
		routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	}
	if s.breaker.State() != "open" {
		t.Fatalf("5xx did not trip breaker, state=%s", s.breaker.State())
	}
}

func TestCircuitBreakerSingleProbeConcurrent(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{err: errors.New("down")}
	s := testServer(t, caller, clock)
	for i := 0; i < 3; i++ {
		routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	}
	clock.Add(31 * time.Second)
	caller.err = nil
	caller.block = make(chan struct{})
	caller.started = make(chan struct{}, 1)
	go routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	<-caller.started
	before := caller.Calls()
	routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	if caller.Calls() != before {
		t.Fatalf("second request during probe called Jev: before=%d after=%d", before, caller.Calls())
	}
	close(caller.block)
}

func TestJevBlackHoleEndpointTripsBreaker(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(net.Conn) { select {} }(c)
		}
	}()
	cfg := DefaultConfig()
	cfg.TimeoutMS = 25
	h := cfg.Harnesses[payload.ShapeClaude]
	h.Enabled = true
	cfg.Harnesses[payload.ShapeClaude] = h
	client := &jev.JevClient{Endpoint: "http://" + ln.Addr().String(), APIKey: "k", AuthScheme: "Bearer", Model: DefaultModel, Timeout: 25 * time.Millisecond, BodyCap: jev.RouterBodyCap}
	s, err := New(Options{Config: cfg, Caller: client, Questions: map[string]any{"tier": map[string]any{"type": "choice", "criteria": map[string]any{"fast": "", "balanced": "", "deep": "", "max": ""}}}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, wire.PathRoute, bytes.NewReader(claudePayload("haiku", "p")))
		req.Header.Set(wire.HeaderProtocol, wire.ProtocolVersion)
		req.Header.Set(wire.HeaderHarness, payload.ShapeClaude)
		req.Header.Set(wire.HeaderDeadline, strconv.FormatInt(time.Now().Add(time.Second).UnixMilli(), 10))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
	}
	if s.breaker.State() != "open" {
		t.Fatalf("black-hole endpoint breaker state = %s, want open", s.breaker.State())
	}
}

func TestVerifyNonceScopeOnceExpiryAndBypass(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{}
	s := testServer(t, caller, clock)
	cfg := s.cfg
	h := cfg.Harnesses[payload.ShapeClaude]
	h.Enabled = false
	h.NoModel.AgentTypes = nil
	cfg.Harnesses[payload.ShapeClaude] = h
	s.cfg = cfg
	nonce := registerNonce(t, s, payload.ShapeClaude, "force:deep")
	rr := routeReq(t, s, payload.ShapeCopilot, copilotPayload("claude-opus-5.5", nonce, false), clock.Now().Add(time.Second))
	if rr.Body.Len() != 0 {
		t.Fatalf("other harness consumed nonce: %q", rr.Body.String())
	}
	rr = routeReq(t, s, payload.ShapeClaude, claudePayload("", "use "+nonce), clock.Now().Add(time.Second))
	if !strings.Contains(rr.Body.String(), `"model":"opus"`) {
		t.Fatalf("force nonce did not emit disabled absent-model override: %s", rr.Body.String())
	}
	rr = routeReq(t, s, payload.ShapeClaude, claudePayload("", "use "+nonce), clock.Now().Add(time.Second))
	if rr.Body.Len() != 0 {
		t.Fatalf("nonce was reusable: %s", rr.Body.String())
	}
	expired := registerNonce(t, s, payload.ShapeClaude, "force:deep")
	clock.Add(11 * time.Minute)
	rr = routeReq(t, s, payload.ShapeClaude, claudePayload("", expired), clock.Now().Add(time.Second))
	if rr.Body.Len() != 0 {
		t.Fatalf("expired nonce applied: %s", rr.Body.String())
	}
	none := registerNonce(t, s, payload.ShapeClaude, "none")
	rr = routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", none), clock.Now().Add(time.Second))
	if rr.Body.Len() != 0 {
		t.Fatalf("none nonce emitted output: %s", rr.Body.String())
	}
}

func registerNonce(t *testing.T, s *Server, harness, action string) string {
	t.Helper()
	body, _ := json.Marshal(wire.VerifyRequest{Harness: harness, Action: action})
	req := httptest.NewRequest(http.MethodPost, wire.PathVerify, bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("verify status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp wire.VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Nonce
}

func TestEarlyExitReasonsSkipJev(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{}
	s := testServer(t, caller, clock)
	cases := []struct {
		name    string
		harness string
		body    []byte
		proto   string
	}{
		{"version skew", payload.ShapeClaude, claudePayload("haiku", "p"), "2"},
		{"harness mismatch", payload.ShapeCopilot, claudePayload("haiku", "p"), wire.ProtocolVersion},
		{"not spawn", payload.ShapeClaude, []byte(`{"tool_name":"Bash","tool_input":{"prompt":"p"}}`), wire.ProtocolVersion},
		{"unmapped model", payload.ShapeClaude, claudePayload("super-haiku", "p"), wire.ProtocolVersion},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, wire.PathRoute, bytes.NewReader(tc.body))
		req.Header.Set(wire.HeaderProtocol, tc.proto)
		req.Header.Set(wire.HeaderHarness, tc.harness)
		req.Header.Set(wire.HeaderDeadline, strconv.FormatInt(clock.Now().Add(time.Second).UnixMilli(), 10))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Body.Len() != 0 {
			t.Fatalf("%s body=%q, want empty", tc.name, rr.Body.String())
		}
	}
	if caller.Calls() != 0 {
		t.Fatalf("early exits called Jev %d times", caller.Calls())
	}
}

func TestHeaderOnlyEarlyExitsRecordDecisionLines(t *testing.T) {
	dir := t.TempDir()
	dlog, _ := telemetry.NewDecisionLog(filepath.Join(dir, "decisions.jsonl"), 1<<20, 3, 64, telemetry.Metrics{})
	t.Cleanup(func() { _ = dlog.Close() })
	clock := newFakeClock()
	caller := &fakeCaller{block: make(chan struct{}), started: make(chan struct{}, 32)}
	s := testServer(t, caller, clock)
	s.log = dlog

	req := httptest.NewRequest(http.MethodPost, wire.PathRoute, bytes.NewReader(claudePayload("haiku", "p")))
	req.Header.Set(wire.HeaderProtocol, "2")
	req.Header.Set(wire.HeaderHarness, payload.ShapeClaude)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
		}()
	}
	for i := 0; i < 32; i++ {
		<-caller.started
	}
	routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	close(caller.block)
	wg.Wait()
	_ = dlog.Close()
	b, err := os.ReadFile(filepath.Join(dir, "decisions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"reason":"version_skew"`) || !strings.Contains(string(b), `"reason":"overloaded"`) {
		t.Fatalf("decision log missing header-only early exits: %s", b)
	}
}

func TestBadJevResponseFailsOpenWithoutBreakerFailure(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{body: []byte(`{"not":"jev"}`)}
	s := testServer(t, caller, clock)
	rr := routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	if rr.Body.Len() != 0 {
		t.Fatalf("bad response body=%q, want empty", rr.Body.String())
	}
	if s.breaker.State() != "closed" {
		t.Fatalf("bad_response tripped breaker, state=%s", s.breaker.State())
	}
}

func TestJevBodyTooLargeIsBadResponseBreakerNeutral(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{err: fmt.Errorf("wrapped: %w", jev.ErrBodyTooLarge)}
	s := testServer(t, caller, clock)
	for i := 0; i < 3; i++ {
		rr := routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
		if rr.Body.Len() != 0 {
			t.Fatalf("body=%q, want empty", rr.Body.String())
		}
	}
	if s.breaker.State() != "closed" {
		t.Fatalf("ErrBodyTooLarge tripped breaker, state=%s", s.breaker.State())
	}
}

func TestJev5xxBodyTooLargeTripsBreaker(t *testing.T) {
	clock := newFakeClock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 70<<10))
	}))
	defer srv.Close()
	cfg := DefaultConfig()
	cfg.Harnesses[payload.ShapeClaude] = router.HarnessConfig{
		Enabled:      true,
		InputAliases: router.DefaultConfig().Harnesses[payload.ShapeClaude].InputAliases,
		Output:       router.DefaultConfig().Harnesses[payload.ShapeClaude].Output,
		NoModel:      router.NoModelConfig{AgentTypes: []string{"research"}, Threshold: 0.9},
	}
	client := &jev.JevClient{Endpoint: srv.URL, APIKey: "k", AuthScheme: "Bearer", Model: DefaultModel, Timeout: time.Second, BodyCap: jev.RouterBodyCap}
	s, err := New(Options{Config: cfg, Caller: client, Clock: clock.Now, Questions: map[string]any{"tier": map[string]any{"type": "choice", "criteria": map[string]any{"fast": "", "balanced": "", "deep": "", "max": ""}}}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		rr := routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
		if rr.Body.Len() != 0 {
			t.Fatalf("5xx oversized response body=%q, want fail-open empty", rr.Body.String())
		}
	}
	if s.breaker.State() != "open" {
		t.Fatalf("5xx oversized Jev response did not trip breaker via status_5xx mechanism: state=%s", s.breaker.State())
	}
}

func TestJevBodyTooLargeStatusClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "200_bad_response", status: http.StatusOK},
		{name: "404_neutral", status: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write(bytes.Repeat([]byte("x"), 70<<10))
			}))
			defer srv.Close()
			cfg := DefaultConfig()
			cfg.Harnesses[payload.ShapeClaude] = router.HarnessConfig{
				Enabled:      true,
				InputAliases: router.DefaultConfig().Harnesses[payload.ShapeClaude].InputAliases,
				Output:       router.DefaultConfig().Harnesses[payload.ShapeClaude].Output,
				NoModel:      router.NoModelConfig{AgentTypes: []string{"research"}, Threshold: 0.9},
			}
			client := &jev.JevClient{Endpoint: srv.URL, APIKey: "k", AuthScheme: "Bearer", Model: DefaultModel, Timeout: time.Second, BodyCap: jev.RouterBodyCap}
			s, err := New(Options{Config: cfg, Caller: client, Clock: clock.Now, Questions: map[string]any{"tier": map[string]any{"type": "choice", "criteria": map[string]any{"fast": "", "balanced": "", "deep": "", "max": ""}}}})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				rr := routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
				if rr.Body.Len() != 0 {
					t.Fatalf("oversized status %d response body=%q, want fail-open empty", tc.status, rr.Body.String())
				}
			}
			if s.breaker.State() != "closed" {
				t.Fatalf("oversized status %d should be breaker-neutral unless 5xx; state=%s", tc.status, s.breaker.State())
			}
		})
	}
}

// errRecorder wraps a JevCaller and records the text of every error it returns.
type errRecorder struct {
	inner JevCaller
	mu    sync.Mutex
	errs  []string
}

func (e *errRecorder) Call(ctx context.Context, state any, q map[string]any) (*jev.CallResult, error) {
	res, err := e.inner.Call(ctx, state, q)
	if err != nil {
		e.mu.Lock()
		e.errs = append(e.errs, err.Error())
		e.mu.Unlock()
	}
	return res, err
}

func (e *errRecorder) Errors() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.errs)
}

func TestJevStalledStatusBodyIsTimeoutFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "4xx", status: http.StatusNotFound},
		{name: "5xx", status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			stop := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Length", "1024")
				w.WriteHeader(tc.status)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				select {
				case <-r.Context().Done():
				case <-stop:
				}
			}))
			defer srv.Close()
			defer close(stop) // runs before srv.Close so a regression fails instead of hanging
			dir := t.TempDir()
			dlog, err := telemetry.NewDecisionLog(filepath.Join(dir, "decisions.jsonl"), 1<<20, 3, 8, telemetry.Metrics{})
			if err != nil {
				t.Fatal(err)
			}
			cfg := DefaultConfig()
			cfg.TimeoutMS = 150
			cfg.JevEndpoint = srv.URL
			cfg.Harnesses[payload.ShapeClaude] = router.HarnessConfig{
				Enabled:      true,
				InputAliases: router.DefaultConfig().Harnesses[payload.ShapeClaude].InputAliases,
				Output:       router.DefaultConfig().Harnesses[payload.ShapeClaude].Output,
				NoModel:      router.NoModelConfig{AgentTypes: []string{"research"}, Threshold: 0.9},
			}
			client := &jev.JevClient{Endpoint: srv.URL, APIKey: "k", AuthScheme: "Bearer", Model: DefaultModel, Timeout: 250 * time.Millisecond, BodyCap: jev.RouterBodyCap}
			rec := &errRecorder{inner: client}
			s, err := New(Options{Config: cfg, Caller: rec, Clock: time.Now, DecisionLog: dlog, Questions: map[string]any{"tier": map[string]any{"type": "choice", "criteria": map[string]any{"fast": "", "balanced": "", "deep": "", "max": ""}}}})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				rr := routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), time.Now().Add(time.Second))
				if rr.Body.Len() != 0 {
					t.Fatalf("stalled status %d body=%q, want fail-open empty", tc.status, rr.Body.String())
				}
			}
			if got := hits.Load(); got != 3 {
				t.Fatalf("stalled status %d: handler hits = %d, want 3 (every request must receive headers so the stall is in the body read)", tc.status, got)
			}
			errs := rec.Errors()
			if len(errs) != 3 {
				t.Fatalf("stalled status %d: recorded %d Jev errors, want 3", tc.status, len(errs))
			}
			for _, e := range errs {
				if !strings.HasPrefix(e, "read response:") {
					t.Fatalf("stalled status %d: Jev error %q did not come from the body-read path", tc.status, e)
				}
			}
			if s.breaker.State() != "open" {
				t.Fatalf("stalled status %d body read error did not count as breaker failure mechanism: state=%s", tc.status, s.breaker.State())
			}
			_ = dlog.Close()
			b, err := os.ReadFile(filepath.Join(dir, "decisions.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(b), `"reason":"jev_error"`); got != 3 {
				t.Fatalf("stalled status %d jev_error lines = %d, want 3; log=%s", tc.status, got, b)
			}
			if got := strings.Count(string(b), `"error_class":"timeout"`); got != 3 {
				t.Fatalf("stalled status %d timeout classification lines = %d, want 3; log=%s", tc.status, got, b)
			}
		})
	}
}

func TestLoadConfigRejectsTimeoutOutsideRequestDeadline(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timeoutMS int
	}{
		{name: "zero", timeoutMS: 0},
		{name: "too_large", timeoutMS: MaxTimeoutMS + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			cfg := DefaultConfig()
			cfg.TimeoutMS = tc.timeoutMS
			b, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), "timeout_ms must be between 1 and 1400 milliseconds") {
				t.Fatalf("LoadConfig timeout_ms=%d err=%v, want clear range rejection", tc.timeoutMS, err)
			}
		})
	}
}

func TestLoadConfigAcceptsTimeoutBoundaries(t *testing.T) {
	for _, ms := range []int{MinTimeoutMS, 1, MaxTimeoutMS, 1400} {
		path := filepath.Join(t.TempDir(), "config.json")
		cfg := DefaultConfig()
		cfg.TimeoutMS = ms
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err != nil {
			t.Fatalf("LoadConfig timeout_ms=%d rejected an in-range value: %v", ms, err)
		}
	}
}

func TestRouteSetsContentLengthForLargeBody(t *testing.T) {
	clock := newFakeClock()
	s := testServer(t, &fakeCaller{}, clock)
	prompt := strings.Repeat("multi-byte-☃️-🙂-", 420)
	rr := routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", prompt), clock.Now().Add(time.Second))
	if rr.Body.Len() < 2<<10 {
		t.Fatalf("test response body is %d bytes, want over chunking threshold", rr.Body.Len())
	}
	if got, want := rr.Header().Get("Content-Length"), strconv.Itoa(rr.Body.Len()); got != want {
		t.Fatalf("route Content-Length = %q, want exact body length %s to avoid chunked responses", got, want)
	}
}

func TestAPIKeyFileRefusesWidePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAPIKey(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("ReadAPIKey wide permissions err=%v, want 0600 refusal", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	key, err := ReadAPIKey(path)
	if err != nil || key != "secret" {
		t.Fatalf("ReadAPIKey strict = %q, %v", key, err)
	}
}

func TestDisabledHarnessLogsWouldBeDecision(t *testing.T) {
	dir := t.TempDir()
	clock := newFakeClock()
	dlog, _ := telemetry.NewDecisionLog(filepath.Join(dir, "decisions.jsonl"), 1<<20, 3, 10, telemetry.Metrics{})
	t.Cleanup(func() { _ = dlog.Close() })
	caller := &fakeCaller{}
	s := testServer(t, caller, clock)
	cfg := s.cfg
	h := cfg.Harnesses[payload.ShapeClaude]
	h.Enabled = false
	cfg.Harnesses[payload.ShapeClaude] = h
	s.cfg, s.log = cfg, dlog
	routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "unique-secret-marker"), clock.Now().Add(time.Second))
	_ = dlog.Close()
	b, err := os.ReadFile(filepath.Join(dir, "decisions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"reason":"disabled"`) || !strings.Contains(string(b), `"decision":"override"`) {
		t.Fatalf("decision line missing disabled would-be override: %s", b)
	}
	if strings.Contains(string(b), "unique-secret-marker") {
		t.Fatalf("prompt leaked into decision log: %s", b)
	}
}

func TestOverBudgetAndNoModelSkipped(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{}
	s := testServer(t, caller, clock)
	cfg := s.cfg
	h := cfg.Harnesses[payload.ShapeClaude]
	h.NoModel.AgentTypes = nil
	cfg.Harnesses[payload.ShapeClaude] = h
	s.cfg = cfg
	routeReq(t, s, payload.ShapeClaude, claudePayload("", "p"), clock.Now().Add(time.Second))
	if caller.Calls() != 0 {
		t.Fatalf("no_model_skipped called Jev")
	}
	routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", strings.Repeat("x", router.Budget+1)), clock.Now().Add(time.Second))
	if caller.Calls() != 0 {
		t.Fatalf("over_budget called Jev")
	}
}

func TestOverloadThirtyThirdRequestSkips(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{block: make(chan struct{}), started: make(chan struct{}, 32)}
	s := testServer(t, caller, clock)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
		}()
	}
	for i := 0; i < 32; i++ {
		<-caller.started
	}
	before := caller.Calls()
	rr := routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	if rr.Body.Len() != 0 || caller.Calls() != before {
		t.Fatalf("overload body=%q calls before=%d after=%d", rr.Body.String(), before, caller.Calls())
	}
	close(caller.block)
	wg.Wait()
}

func TestPeerUIDCheckBeforeHandler(t *testing.T) {
	clock := newFakeClock()
	s := testServer(t, &fakeCaller{}, clock)
	dir, err := os.MkdirTemp("/tmp", "mr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := s.Listen(filepath.Join(dir, "r.sock"), func(*net.UnixConn) (uint32, error) { return currentUID() + 1, nil })
	if err != nil {
		t.Fatal(err)
	}
	var ran atomic.Bool
	httpSrv := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran.Store(true) })}
	go httpSrv.Serve(ln)
	defer httpSrv.Close()
	c, err := net.Dial("unix", filepath.Join(dir, "r.sock"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("GET /v1/health HTTP/1.1\r\nHost: x\r\n\r\n"))
	_, _ = io.ReadAll(c)
	_ = c.Close()
	time.Sleep(20 * time.Millisecond)
	if ran.Load() {
		t.Fatal("handler ran for mismatching peer uid")
	}
	if ln.Rejected.Load() == 0 {
		t.Fatal("mismatching peer uid was not counted")
	}
}

func TestEndToEndUnixSocketClaudeAndCopilot(t *testing.T) {
	clock := newFakeClock()
	s := testServer(t, &fakeCaller{}, clock)
	dir, err := os.MkdirTemp("/tmp", "mr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "r.sock")
	ln, err := s.Listen(sock, func(*net.UnixConn) (uint32, error) { return currentUID(), nil })
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := s.HTTPServer()
	go httpSrv.Serve(ln)
	defer httpSrv.Close()
	do := func(h string, body []byte) string {
		client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}
		req, _ := http.NewRequest(http.MethodPost, "http://unix"+wire.PathRoute, bytes.NewReader(body))
		req.Header.Set(wire.HeaderProtocol, wire.ProtocolVersion)
		req.Header.Set(wire.HeaderHarness, h)
		req.Header.Set(wire.HeaderDeadline, strconv.FormatInt(clock.Now().Add(time.Second).UnixMilli(), 10))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := do(payload.ShapeClaude, claudePayload("haiku", "p")); !strings.Contains(got, `"updatedInput"`) || !strings.Contains(got, `"model":"opus"`) {
		t.Fatalf("claude output = %s", got)
	}
	if got := do(payload.ShapeCopilot, copilotPayload("claude-haiku-4.5", "p", true)); !strings.Contains(got, `"modifiedArgs"`) || !strings.Contains(got, `"model":"claude-opus-5.5"`) {
		t.Fatalf("copilot output = %s", got)
	}
}

func TestPromptTextNotInDecisionLineOrSpanAttributes(t *testing.T) {
	const marker = "do-not-log-prompt-marker-9c26"
	dir := t.TempDir()
	dlog, _ := telemetry.NewDecisionLog(filepath.Join(dir, "decisions.jsonl"), 1<<20, 3, 10, telemetry.Metrics{})
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exp)))
	defer tp.Shutdown(context.Background())
	clock := newFakeClock()
	s := testServer(t, &fakeCaller{}, clock)
	s.log = dlog
	s.tel = &telemetry.System{Tracer: tp.Tracer("test")}
	routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", marker), clock.Now().Add(time.Second))
	_ = dlog.Close()
	b, err := os.ReadFile(filepath.Join(dir, "decisions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), marker) {
		t.Fatalf("prompt marker leaked into decision line: %s", b)
	}
	for _, sp := range exp.GetSpans() {
		for _, a := range sp.Attributes {
			if strings.Contains(a.Value.AsString(), marker) {
				t.Fatalf("prompt marker leaked into span %s attribute %s", sp.Name, a.Key)
			}
		}
	}
}

func TestTelemetryExporterBlackholeDoesNotMateriallyChangeLatency(t *testing.T) {
	measure := func(disabled bool, endpoint string) time.Duration {
		t.Helper()
		t.Setenv("OTEL_SDK_DISABLED", strconv.FormatBool(disabled))
		t.Setenv("MODEL_ROUTER_OTEL_TEST_FAST", "1")
		if endpoint != "" {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
		}
		tel, err := telemetry.Setup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_ = tel.Shutdown(ctx)
		}()
		clock := newFakeClock()
		s := testServer(t, &fakeCaller{}, clock)
		s.tel = tel
		var samples []time.Duration
		for i := 0; i < 400; i++ {
			start := time.Now()
			routeReq(t, s, payload.ShapeClaude, claudePayload("opus", "p"), clock.Now().Add(time.Second))
			samples = append(samples, time.Since(start))
		}
		time.Sleep(20 * time.Millisecond)
		slices.Sort(samples)
		return samples[len(samples)/2]
	}
	disabled := measure(true, "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepts atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			go func() { select {} }()
			_ = c
		}
	}()
	blackhole := measure(false, "http://"+ln.Addr().String())
	if accepts.Load() == 0 {
		t.Fatal("black-holed telemetry test made no export attempts")
	}
	if blackhole > disabled+2*time.Millisecond {
		t.Fatalf("black-holed telemetry median %s, disabled median %s: exporter affected request latency", blackhole, disabled)
	}
}

func BenchmarkRouteSpawnExcludingJev(b *testing.B) {
	clock := newFakeClock()
	cfg := DefaultConfig()
	h := cfg.Harnesses[payload.ShapeClaude]
	h.Enabled = true
	cfg.Harnesses[payload.ShapeClaude] = h
	s, err := New(Options{Config: cfg, Caller: &fakeCaller{}, Clock: clock.Now, Questions: map[string]any{"tier": map[string]any{"type": "choice", "criteria": map[string]any{"fast": "", "balanced": "", "deep": "", "max": ""}}}})
	if err != nil {
		b.Fatal(err)
	}
	body := claudePayload("haiku", "p")
	deadline := clock.Now().Add(time.Second)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, wire.PathRoute, bytes.NewReader(body))
		req.Header.Set(wire.HeaderProtocol, wire.ProtocolVersion)
		req.Header.Set(wire.HeaderHarness, payload.ShapeClaude)
		req.Header.Set(wire.HeaderDeadline, strconv.FormatInt(deadline.UnixMilli(), 10))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		if rr.Code != 200 {
			b.Fatal(rr.Code)
		}
	}
}

func TestClientCancelIsBreakerNeutralButJevTimeoutIsFailure(t *testing.T) {
	cancelOnce := func(t *testing.T, s *Server, started chan struct{}) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, wire.PathRoute, bytes.NewReader(claudePayload("haiku", "p"))).WithContext(ctx)
		req.Header.Set(wire.HeaderProtocol, wire.ProtocolVersion)
		req.Header.Set(wire.HeaderHarness, payload.ShapeClaude)
		req.Header.Set(wire.HeaderDeadline, strconv.FormatInt(time.Now().Add(time.Second).UnixMilli(), 10))
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		}()
		select {
		case <-started:
		case <-done:
			t.Fatalf("request returned without reaching Jev (breaker open?): breaker=%s", s.breaker.State())
		case <-time.After(2 * time.Second):
			t.Fatal("request never reached Jev")
		}
		cancel()
		<-done
	}
	t.Run("client_cancel", func(t *testing.T) {
		clock := newFakeClock()
		caller := &fakeCaller{block: make(chan struct{}), started: make(chan struct{}, 1)}
		dir := t.TempDir()
		dlog, err := telemetry.NewDecisionLog(filepath.Join(dir, "d.jsonl"), 1<<20, 3, 8, telemetry.Metrics{})
		if err != nil {
			t.Fatal(err)
		}
		s := testServer(t, caller, clock)
		s.log = dlog
		for i := 0; i < 4; i++ {
			cancelOnce(t, s, caller.started)
		}
		if st := s.breaker.State(); st != "closed" {
			t.Fatalf("client disconnects must not count as Jev failures: breaker=%s", st)
		}
		_ = dlog.Close()
		b, _ := os.ReadFile(filepath.Join(dir, "d.jsonl"))
		if got := strings.Count(string(b), `"error_class":"canceled"`); got != 4 {
			t.Fatalf("canceled lines = %d, want 4; log=%s", got, b)
		}
	})
	t.Run("jev_deadline", func(t *testing.T) {
		clock := newFakeClock()
		caller := &fakeCaller{block: make(chan struct{})}
		s := testServer(t, caller, clock)
		for i := 0; i < 3; i++ {
			routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(150*time.Millisecond))
		}
		if st := s.breaker.State(); st != "open" {
			t.Fatalf("Jev deadline timeouts must trip the breaker: breaker=%s", st)
		}
	})
}

func TestHalfOpenProbeCancelledByClientDoesNotWedgeBreaker(t *testing.T) {
	clock := newFakeClock()
	caller := &fakeCaller{err: errors.New("jev down")}
	s := testServer(t, caller, clock)
	for i := 0; i < 3; i++ {
		routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	}
	if st := s.breaker.State(); st != "open" {
		t.Fatalf("setup: breaker=%s, want open", st)
	}
	clock.Add(31 * time.Second)
	caller.mu.Lock()
	caller.err = nil
	caller.block = make(chan struct{})
	caller.started = make(chan struct{}, 1)
	caller.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, wire.PathRoute, bytes.NewReader(claudePayload("haiku", "p"))).WithContext(ctx)
	req.Header.Set(wire.HeaderProtocol, wire.ProtocolVersion)
	req.Header.Set(wire.HeaderHarness, payload.ShapeClaude)
	req.Header.Set(wire.HeaderDeadline, strconv.FormatInt(clock.Now().Add(time.Second).UnixMilli(), 10))
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}()
	select {
	case <-caller.started:
	case <-time.After(2 * time.Second):
		t.Fatal("half-open probe never reached Jev")
	}
	cancel()
	<-done
	caller.mu.Lock()
	caller.block = nil
	caller.mu.Unlock()
	before := caller.Calls()
	routeReq(t, s, payload.ShapeClaude, claudePayload("haiku", "p"), clock.Now().Add(time.Second))
	if caller.Calls() != before+1 {
		t.Fatalf("request after a client-cancelled half-open probe never reached Jev (probeInFlight stuck): breaker=%s", s.breaker.State())
	}
}
