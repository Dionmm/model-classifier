package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dionmm/model-classifier/internal/jev"
	"github.com/Dionmm/model-classifier/internal/payload"
	"github.com/Dionmm/model-classifier/internal/router"
	"github.com/Dionmm/model-classifier/internal/telemetry"
	"github.com/Dionmm/model-classifier/internal/wire"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type JevCaller interface {
	Call(context.Context, any, map[string]any) (*jev.CallResult, error)
}

type Options struct {
	Config        Config
	Questions     map[string]any
	Caller        JevCaller
	Telemetry     *telemetry.System
	DecisionLog   *telemetry.DecisionLog
	Clock         func() time.Time
	PeerUIDGetter PeerUIDGetter
	SocketPath    string
	APIKey        string
}

type Server struct {
	cfg       Config
	questions map[string]any
	caller    JevCaller
	tel       *telemetry.System
	log       *telemetry.DecisionLog
	clock     func() time.Time
	breaker   *circuitBreaker
	sem       chan struct{}
	nonces    *nonceStore
	active    atomic.Int64
}

func New(o Options) (*Server, error) {
	cfg := o.Config
	if cfg.JevEndpoint == "" {
		cfg = DefaultConfig()
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if err := cfg.Config.Validate(); err != nil {
		return nil, err
	}
	q := o.Questions
	if q == nil {
		var err error
		if cfg.Questions != "" {
			f, err := os.Open(cfg.Questions)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			q, err = jev.LoadQuestions(f)
		} else {
			q, err = jev.EmbeddedQuestions()
		}
		if err != nil {
			return nil, err
		}
	}
	clock := o.Clock
	if clock == nil {
		clock = time.Now
	}
	caller := o.Caller
	if caller == nil {
		apiKey := o.APIKey
		if apiKey == "" {
			var err error
			apiKey, err = ReadAPIKey(cfg.APIKeyFile)
			if err != nil {
				return nil, err
			}
		}
		caller = &jev.JevClient{Endpoint: cfg.JevEndpoint, APIKey: apiKey, AuthScheme: "Bearer", Model: cfg.PinnedModel, Timeout: cfg.Timeout(), BodyCap: jev.RouterBodyCap}
	}
	return &Server{
		cfg: cfg, questions: q, caller: caller, tel: o.Telemetry, log: o.DecisionLog,
		clock: clock, breaker: newBreaker(clock), sem: make(chan struct{}, 32), nonces: newNonceStore(clock),
	}, nil
}

func ReadAPIKey(path string) (string, error) {
	if path == "" {
		return strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")), nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("api key file %s is not regular", path)
	}
	if st.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("api key file %s must be 0600 or stricter", path)
	}
	if sysUID(path) != os.Getuid() {
		return "", fmt.Errorf("api key file %s must be owned by current user", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(wire.PathRoute, s.route)
	mux.HandleFunc(wire.PathVerify, s.verify)
	mux.HandleFunc(wire.PathHealth, s.health)
	return mux
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tr := trace.NewNoopTracerProvider().Tracer("noop")
	metrics := telemetry.Metrics{}
	if s.tel != nil {
		tr = s.tel.Tracer
		metrics = s.tel.Metrics
	}
	ctx, span := tr.Start(ctx, "route")
	defer span.End()
	w.Header().Set("Content-Type", "application/json")
	recordHeader := func(reason string) {
		harness := r.Header.Get(wire.HeaderHarness)
		metrics.RecordDecision(ctx, reason, harness)
		span.SetAttributes(attribute.String("harness", harness), attribute.String("reason", reason))
		if s.log != nil {
			s.log.Record(telemetry.DecisionLine{TS: s.clock().UTC(), Harness: harness, Reason: reason})
		}
	}
	defer func() {
		if recover() != nil {
			metrics.RecordDecision(ctx, "panic", "")
			w.WriteHeader(http.StatusOK)
		}
	}()
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Header.Get(wire.HeaderProtocol) != wire.ProtocolVersion {
		recordHeader("version_skew")
		w.WriteHeader(http.StatusOK)
		return
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		recordHeader("overloaded")
		w.WriteHeader(http.StatusOK)
		return
	}
	metrics.AddInflight(ctx, 1)
	defer metrics.AddInflight(ctx, -1)
	raw, err := readCapped(r.Body, wire.MaxPayloadBytes)
	if err != nil {
		metrics.RecordDecision(ctx, "not_spawn", r.Header.Get(wire.HeaderHarness))
		w.WriteHeader(http.StatusOK)
		return
	}
	sp, ok := payload.ExtractSpawn(raw)
	if !ok {
		metrics.RecordDecision(ctx, "not_spawn", r.Header.Get(wire.HeaderHarness))
		w.WriteHeader(http.StatusOK)
		return
	}
	line := telemetry.DecisionLine{TS: s.clock().UTC(), Harness: sp.Shape, SessionID: sp.SessionID, ToolUseID: sp.ToolUseID, OrchestratorModel: sp.Model.Value}
	record := func(reason string) {
		line.Reason = reason
		metrics.RecordDecision(ctx, reason, sp.Shape)
		if s.log != nil {
			s.log.Record(line)
		}
	}
	hdrHarness := r.Header.Get(wire.HeaderHarness)
	if (hdrHarness != payload.ShapeClaude && hdrHarness != payload.ShapeCopilot) || hdrHarness != sp.Shape {
		line.Harness = hdrHarness
		record("harness_mismatch")
		w.WriteHeader(http.StatusOK)
		return
	}
	span.SetAttributes(attribute.String("harness", sp.Shape), attribute.String("session_id", sp.SessionID), attribute.String("tool_use_id", sp.ToolUseID))
	if nonce, action, ok := s.nonces.consume(sp.Shape, sp.Prompt); ok {
		line.Nonce = nonce
		line.Decision = action
		record("verify")
		if strings.HasPrefix(action, "force:") {
			tier := strings.TrimPrefix(action, "force:")
			out := s.cfg.Harnesses[sp.Shape].Output[tier]
			if out != "" {
				body, err := router.BuildOutput(sp.Shape, sp.Args, out)
				if err == nil {
					writeBody(w, body)
					return
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if apiKeyMissing(s.caller) {
		record("no_api_key")
		w.WriteHeader(http.StatusOK)
		return
	}
	hcfg, ok := s.cfg.Harnesses[sp.Shape]
	if !ok {
		record("harness_mismatch")
		w.WriteHeader(http.StatusOK)
		return
	}
	pc := router.Precheck(hcfg, sp.Model, sp.SubagentType)
	if !pc.OK {
		record(pc.Reason)
		w.WriteHeader(http.StatusOK)
		return
	}
	line.OrchestratorTier = pc.FromTier
	state, _, n, err := router.BuildState(sp.SubagentType, sp.Description, sp.Prompt)
	if err != nil || router.OverBudget(n) {
		line.StateBytes = n
		record("over_budget")
		w.WriteHeader(http.StatusOK)
		return
	}
	line.StateBytes = n
	if !s.breaker.Allow() {
		record("circuit_open")
		w.WriteHeader(http.StatusOK)
		return
	}
	deadline, ok := s.deadline(r)
	if !ok {
		s.breaker.Ignore()
		record("deadline")
		w.WriteHeader(http.StatusOK)
		return
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	jctx, jspan := tr.Start(callCtx, "jev.call", trace.WithAttributes(attribute.Int("state_bytes", n), attribute.String("orchestrator_tier", pc.FromTier)))
	start := s.clock()
	res, err := s.caller.Call(jctx, state, s.questions)
	lat := float64(s.clock().Sub(start).Microseconds()) / 1000
	line.JevLatencyMS = lat
	metrics.RecordJevLatency(ctx, lat)
	if err != nil {
		line.ErrorClass = classifyError(err, jctx)
		jspan.RecordError(err)
		jspan.SetStatus(codes.Error, line.ErrorClass)
		if res != nil {
			jspan.SetAttributes(attribute.Int("http.status_code", res.Status))
		}
		jspan.End()
		switch {
		case line.ErrorClass == "canceled":
			// The hook client went away; that says nothing about Jev's health.
			s.breaker.Ignore()
			record("jev_error")
			w.WriteHeader(http.StatusOK)
			return
		case res != nil && res.Status >= 500 && errors.Is(err, jev.ErrBodyTooLarge):
			s.breaker.Failure(span)
			line.ErrorClass = classifyStatus(res.Status)
			record("jev_error")
			w.WriteHeader(http.StatusOK)
			return
		case res != nil && res.Status >= 400 && errors.Is(err, jev.ErrBodyTooLarge):
			s.breaker.Ignore()
			line.ErrorClass = classifyStatus(res.Status)
			record("jev_error")
			w.WriteHeader(http.StatusOK)
			return
		case res != nil && res.Status == http.StatusOK && errors.Is(err, jev.ErrBodyTooLarge):
			s.breaker.Ignore()
			line.ErrorClass = "bad_response"
			record("bad_response")
			w.WriteHeader(http.StatusOK)
			return
		case errors.Is(err, jev.ErrBodyTooLarge):
			s.breaker.Ignore()
			line.ErrorClass = "bad_response"
			record("bad_response")
			w.WriteHeader(http.StatusOK)
			return
		}
		s.breaker.Failure(span)
		record("jev_error")
		w.WriteHeader(http.StatusOK)
		return
	}
	jspan.SetAttributes(attribute.Int("http.status_code", res.Status))
	jspan.End()
	if res.Status != http.StatusOK {
		line.ErrorClass = classifyStatus(res.Status)
		if res.Status >= 500 {
			s.breaker.Failure(span)
		} else {
			s.breaker.Ignore()
		}
		record("jev_error")
		w.WriteHeader(http.StatusOK)
		return
	}
	ans, err := jev.Validate(res.Body, s.cfg.PinnedModel)
	if err != nil {
		s.breaker.Ignore()
		line.ErrorClass = "bad_response"
		record("bad_response")
		w.WriteHeader(http.StatusOK)
		return
	}
	s.breaker.Success(span)
	line.JevTier, line.Confidence, line.Probabilities, line.JevModel, line.InputTokens = ans.Choice, ans.Confidence, ans.Probabilities, ans.Model, ans.InputTokens
	dec := router.Decide(hcfg, s.cfg.Thresholds, sp.Model, sp.SubagentType, ans)
	line.Decision = dec.Reason
	line.JevTier = dec.ToTier
	if !dec.Override {
		record(dec.Reason)
		w.WriteHeader(http.StatusOK)
		return
	}
	if !hcfg.Enabled {
		record("disabled")
		w.WriteHeader(http.StatusOK)
		return
	}
	if hcfg.Output[dec.ToTier] == "" {
		record("no_output_model")
		w.WriteHeader(http.StatusOK)
		return
	}
	body, err := router.BuildOutput(sp.Shape, sp.Args, dec.OutModel)
	if err != nil {
		record("bad_response")
		w.WriteHeader(http.StatusOK)
		return
	}
	record(dec.Reason)
	writeBody(w, body)
}

func (s *Server) deadline(r *http.Request) (time.Time, bool) {
	v := r.Header.Get(wire.HeaderDeadline)
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, false
	}
	client := time.UnixMilli(ms).Add(-100 * time.Millisecond)
	if !client.After(s.clock()) {
		return time.Time{}, false
	}
	cfg := s.clock().Add(s.cfg.Timeout())
	if client.Before(cfg) {
		return client, true
	}
	return cfg, true
}

func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
		return
	}
	var req wire.VerifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	resp, err := s.nonces.register(req.Harness, req.Action)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, resp)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	h := map[string]bool{}
	for k, v := range s.cfg.Harnesses {
		h[k] = v.Enabled
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, wire.HealthResponse{Protocol: 1, Version: Version, Harnesses: h, BreakerState: s.breaker.State(), HasAPIKey: !apiKeyMissing(s.caller)})
}

func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	b = append(b, '\n')
	writeBody(w, b)
}

func writeBody(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 500 * time.Millisecond,
		ReadTimeout:       1500 * time.Millisecond,
		WriteTimeout:      1500 * time.Millisecond,
		MaxHeaderBytes:    16 << 10,
	}
}

func (s *Server) Listen(path string, getUID PeerUIDGetter) (*UIDListener, error) {
	if path == "" {
		path = wire.SocketPath()
	}
	if err := secureSocketDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ua, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, err
	}
	ul, err := net.ListenUnix("unix", ua)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = ul.Close()
		return nil, err
	}
	return &UIDListener{UnixListener: ul, UID: currentUID(), GetUID: getUID}, nil
}

func (s *Server) WarmupLoop(ctx context.Context) {
	s.warmup(ctx)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.warmup(ctx)
		}
	}
}

func (s *Server) warmup(ctx context.Context) {
	w, ok := s.caller.(interface{ Warm(context.Context) error })
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_ = w.Warm(cctx)
}

func readCapped(r io.Reader, cap int) ([]byte, error) {
	var b bytes.Buffer
	n, err := io.Copy(&b, io.LimitReader(r, int64(cap)+1))
	if err != nil {
		return nil, err
	}
	if n > int64(cap) {
		return nil, fmt.Errorf("body exceeds %d", cap)
	}
	return b.Bytes(), nil
}

func classifyError(err error, ctx context.Context) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
		return "canceled"
	}
	return "transport"
}

func classifyStatus(st int) string {
	switch {
	case st == http.StatusTooManyRequests:
		return "status_429"
	case st >= 500:
		return "status_5xx"
	case st >= 400:
		return "status_4xx"
	default:
		return "status"
	}
}

func apiKeyMissing(c JevCaller) bool {
	jc, ok := c.(*jev.JevClient)
	return ok && strings.TrimSpace(jc.APIKey) == ""
}

func secureSocketDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	if sysUID(dir) != os.Getuid() {
		return fmt.Errorf("%s must be owned by current user", dir)
	}
	return nil
}

type nonceEntry struct {
	Harness string
	Action  string
	Expires time.Time
}

type nonceStore struct {
	mu    sync.Mutex
	m     map[string]nonceEntry
	clock func() time.Time
}

func newNonceStore(clock func() time.Time) *nonceStore {
	return &nonceStore{m: map[string]nonceEntry{}, clock: clock}
}

func (n *nonceStore) register(harness, action string) (wire.VerifyResponse, error) {
	if harness != payload.ShapeClaude && harness != payload.ShapeCopilot {
		return wire.VerifyResponse{}, fmt.Errorf("unknown harness")
	}
	if action != "none" && !strings.HasPrefix(action, "force:") {
		return wire.VerifyResponse{}, fmt.Errorf("bad action")
	}
	if strings.HasPrefix(action, "force:") {
		tier := strings.TrimPrefix(action, "force:")
		found := false
		for _, t := range jev.Tiers {
			found = found || t == tier
		}
		if !found {
			return wire.VerifyResponse{}, fmt.Errorf("bad tier")
		}
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return wire.VerifyResponse{}, err
	}
	nonce := "mr-verify-" + hex.EncodeToString(raw[:])
	exp := n.clock().Add(10 * time.Minute)
	n.mu.Lock()
	defer n.mu.Unlock()
	for k, e := range n.m {
		if n.clock().After(e.Expires) {
			delete(n.m, k)
		}
	}
	for len(n.m) >= 16 {
		for k := range n.m {
			delete(n.m, k)
			break
		}
	}
	n.m[nonce] = nonceEntry{Harness: harness, Action: action, Expires: exp}
	return wire.VerifyResponse{Nonce: nonce, ExpiresAt: exp}, nil
}

func (n *nonceStore) consume(harness, prompt string) (string, string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.clock()
	for nonce, e := range n.m {
		if now.After(e.Expires) {
			delete(n.m, nonce)
			continue
		}
		if e.Harness == harness && strings.Contains(prompt, nonce) {
			delete(n.m, nonce)
			return nonce, e.Action, true
		}
	}
	return "", "", false
}
