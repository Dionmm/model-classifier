package jev

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	DefaultBodyCap  = int64(4 << 20)
	RouterBodyCap   = int64(64 << 10)
)

var Tiers = []string{"fast", "balanced", "deep", "max"}

var ErrBodyTooLarge = errors.New("jev response body too large")

type BodyTooLargeError struct {
	Status int
	Cap    int64
}

func (e *BodyTooLargeError) Error() string {
	return fmt.Sprintf("%v: status %d exceeds %d bytes", ErrBodyTooLarge, e.Status, e.Cap)
}

func (e *BodyTooLargeError) Unwrap() error { return ErrBodyTooLarge }

//go:embed questions-v4.json
var embeddedQuestions []byte

func EmbeddedQuestionsBytes() []byte {
	return append([]byte(nil), embeddedQuestions...)
}

func EmbeddedQuestions() (map[string]any, error) {
	return LoadQuestions(bytes.NewReader(embeddedQuestions))
}

type Timing struct {
	DNSMs     float64 `json:"dns_ms"`
	ConnectMs float64 `json:"connect_ms"`
	TLSMs     float64 `json:"tls_ms"`
	TTFBMs    float64 `json:"ttfb_ms"`
	TotalMs   float64 `json:"total_ms"`
	Reused    bool    `json:"reused"`
}

type timer struct {
	mu                                   sync.Mutex
	t                                    Timing
	start, dnsStart, connStart, tlsStart time.Time
}

func msSince(from time.Time) float64 { return float64(time.Since(from).Microseconds()) / 1000 }

func (tm *timer) markStart() {
	tm.mu.Lock()
	tm.start = time.Now()
	tm.mu.Unlock()
}

func (tm *timer) snapshot() Timing {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.t.TotalMs = msSince(tm.start)
	return tm.t
}

func (tm *timer) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			tm.mu.Lock()
			tm.dnsStart = time.Now()
			tm.mu.Unlock()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			tm.mu.Lock()
			tm.t.DNSMs = msSince(tm.dnsStart)
			tm.mu.Unlock()
		},
		ConnectStart: func(string, string) {
			tm.mu.Lock()
			tm.connStart = time.Now()
			tm.mu.Unlock()
		},
		ConnectDone: func(string, string, error) {
			tm.mu.Lock()
			tm.t.ConnectMs = msSince(tm.connStart)
			tm.mu.Unlock()
		},
		TLSHandshakeStart: func() {
			tm.mu.Lock()
			tm.tlsStart = time.Now()
			tm.mu.Unlock()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			tm.mu.Lock()
			tm.t.TLSMs = msSince(tm.tlsStart)
			tm.mu.Unlock()
		},
		GotConn: func(i httptrace.GotConnInfo) {
			tm.mu.Lock()
			tm.t.Reused = i.Reused
			tm.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			tm.mu.Lock()
			tm.t.TTFBMs = msSince(tm.start)
			tm.mu.Unlock()
		},
	}
}

type JevClient struct {
	Endpoint   string
	APIKey     string
	AuthScheme string
	Model      string
	Timeout    time.Duration
	Fresh      bool
	BodyCap    int64

	once    sync.Once
	shared  *http.Client
	lastUse atomic.Int64
}

type CallResult struct {
	Status     int
	RetryAfter time.Duration
	Body       []byte
	Timing     Timing
}

func (c *JevClient) httpClient() *http.Client {
	if c.Fresh {
		return &http.Client{Timeout: c.Timeout, Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, ForceAttemptHTTP2: true,
		}}
	}
	c.once.Do(func() {
		c.shared = &http.Client{Timeout: c.Timeout, Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 4,
		}}
	})
	return c.shared
}

func (c *JevClient) Warm(ctx context.Context) error {
	now := time.Now()
	last := c.lastUse.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) < 60*time.Second {
		return nil
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.Scheme+"://"+u.Host+"/", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	c.lastUse.Store(now.UnixNano())
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

func (c *JevClient) Call(ctx context.Context, state any, questions map[string]any) (*CallResult, error) {
	body, err := json.Marshal(map[string]any{"state": state, "model": c.Model, "questions": questions})
	if err != nil {
		return nil, err
	}
	tm := &timer{}
	ctx = httptrace.WithClientTrace(ctx, tm.trace())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	auth := c.APIKey
	if c.AuthScheme != "" {
		auth = c.AuthScheme + " " + c.APIKey
	}
	req.Header.Set("Authorization", auth)
	hc := c.httpClient()
	if c.Fresh {
		defer hc.CloseIdleConnections()
	}
	c.lastUse.Store(time.Now().UnixNano())
	tm.markStart()
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	defer c.lastUse.Store(time.Now().UnixNano())
	cap := c.BodyCap
	if cap <= 0 {
		cap = DefaultBodyCap
	}
	rb, err := readCapped(resp.Body, cap)
	timing := tm.snapshot()
	res := &CallResult{Status: resp.StatusCode, Body: rb, Timing: timing}
	if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && s > 0 {
		res.RetryAfter = time.Duration(s) * time.Second
	}
	if err != nil {
		if errors.Is(err, ErrBodyTooLarge) {
			return res, fmt.Errorf("read response: %w", &BodyTooLargeError{Status: resp.StatusCode, Cap: cap})
		}
		return res, fmt.Errorf("read response: %w", err)
	}
	return res, nil
}

func readCapped(r io.Reader, cap int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, cap+1))
	if err != nil {
		return nil, err
	}
	if n > cap {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrBodyTooLarge, cap)
	}
	return buf.Bytes(), nil
}

type JevResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type Answer struct {
	Choice        string
	Confidence    float64
	Probabilities map[string]float64
	Model         string
	InputTokens   int
}

func Validate(body []byte, pinnedModel string) (Answer, error) {
	if int64(len(body)) > RouterBodyCap {
		return Answer{}, fmt.Errorf("body exceeds %d bytes", RouterBodyCap)
	}
	var jr JevResponse
	if err := json.Unmarshal(body, &jr); err != nil {
		return Answer{}, fmt.Errorf("decode response: %w", err)
	}
	if jr.Model != pinnedModel {
		return Answer{}, fmt.Errorf("model %q != pinned %q", jr.Model, pinnedModel)
	}
	var tier struct {
		Type          string             `json:"type"`
		Choice        string             `json:"choice"`
		Confidence    float64            `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	}
	if err := json.Unmarshal(jr.Answers["tier"], &tier); err != nil {
		return Answer{}, fmt.Errorf("tier answer: %w", err)
	}
	if tier.Type != "choice" {
		return Answer{}, fmt.Errorf("tier type %q is not choice", tier.Type)
	}
	if !slices.Contains(Tiers, tier.Choice) {
		return Answer{}, fmt.Errorf("choice %q is not one of %v", tier.Choice, Tiers)
	}
	if !finite01(tier.Confidence) {
		return Answer{}, fmt.Errorf("confidence %v outside [0,1]", tier.Confidence)
	}
	if len(tier.Probabilities) != len(Tiers) {
		return Answer{}, fmt.Errorf("probabilities have %d keys, want %d", len(tier.Probabilities), len(Tiers))
	}
	sum := 0.0
	chosen := tier.Probabilities[tier.Choice]
	for _, k := range Tiers {
		p, ok := tier.Probabilities[k]
		if !ok {
			return Answer{}, fmt.Errorf("probabilities missing %q", k)
		}
		if !finite01(p) {
			return Answer{}, fmt.Errorf("probability %q=%v outside [0,1]", k, p)
		}
		sum += p
		if p > chosen {
			return Answer{}, fmt.Errorf("choice %q probability %.2f is below %q %.2f", tier.Choice, chosen, k, p)
		}
	}
	if math.Abs(sum-1) > 0.02 {
		return Answer{}, fmt.Errorf("probabilities sum %.2f, want within 0.02 of 1", sum)
	}
	return Answer{
		Choice: tier.Choice, Confidence: tier.Confidence, Probabilities: tier.Probabilities,
		Model: jr.Model, InputTokens: jr.Usage.InputTokens,
	}, nil
}

func finite01(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1
}

func LoadQuestions(r io.Reader) (map[string]any, error) {
	var q map[string]any
	if err := json.NewDecoder(r).Decode(&q); err != nil {
		return nil, fmt.Errorf("questions: %w", err)
	}
	tier, _ := q["tier"].(map[string]any)
	if tier == nil || tier["type"] != "choice" {
		return nil, fmt.Errorf(`questions: need a "tier" question of type "choice"`)
	}
	crit, _ := tier["criteria"].(map[string]any)
	if len(crit) < 2 {
		return nil, fmt.Errorf("questions: tier needs at least two criteria")
	}
	for k := range crit {
		if !slices.Contains(Tiers, k) {
			return nil, fmt.Errorf("questions: tier option %q is not one of %v", k, Tiers)
		}
	}
	return q, nil
}
