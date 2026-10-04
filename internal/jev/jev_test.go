package jev

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func validBody(choice string, conf float64, probs string) []byte {
	return []byte(fmt.Sprintf(`{"model":"jev-1.13.0","answers":{"long_context":{"type":"noul","noul":0.19},"tier":{"type":"choice","choice":%q,"confidence":%g,"probabilities":%s}},"usage":{"input_tokens":123,"output_tokens":4}}`, choice, conf, probs))
}

func TestValidateAcceptsRealShape(t *testing.T) {
	body := validBody("fast", 0.63, `{"max":0.0,"fast":0.73,"deep":0.22,"balanced":0.05}`)
	ans, err := Validate(body, "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Choice != "fast" || ans.Confidence != 0.63 || ans.Model != "jev-1.13.0" || ans.InputTokens != 123 {
		t.Fatalf("answer = %+v", ans)
	}
}

func TestValidateRejectsEachRule(t *testing.T) {
	validProbs := `{"fast":0.7,"balanced":0.1,"deep":0.1,"max":0.1}`
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"wrong model":      {strings.Replace(string(validBody("fast", 0.9, validProbs)), "jev-1.13.0", "jev-1.12.0", 1), "model"},
		"wrong type":       {`{"model":"jev-1.13.0","answers":{"tier":{"type":"noul","choice":"fast","confidence":0.9,"probabilities":` + validProbs + `}}}`, "not choice"},
		"unknown choice":   {string(validBody("huge", 0.9, validProbs)), "choice"},
		"confidence low":   {string(validBody("fast", -0.1, validProbs)), "confidence"},
		"confidence high":  {string(validBody("fast", 1.1, validProbs)), "confidence"},
		"missing keys":     {string(validBody("fast", 0.9, `{"fast":1}`)), "keys"},
		"extra key":        {string(validBody("fast", 0.9, `{"fast":0.7,"balanced":0.1,"deep":0.1,"max":0.1,"huge":0}`)), "keys"},
		"probability high": {string(validBody("fast", 0.9, `{"fast":1.2,"balanced":0,"deep":0,"max":0}`)), "outside"},
		"bad sum":          {string(validBody("fast", 0.9, `{"fast":0.7,"balanced":0.2,"deep":0.1,"max":0.05}`)), "sum"},
		"not highest":      {string(validBody("deep", 0.9, validProbs)), "below"},
		"bad json":         {`{`, "decode"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Validate([]byte(tc.body), "jev-1.13.0")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateRejectsRouterBodyCap(t *testing.T) {
	_, err := Validate(bytes.Repeat([]byte("x"), 64<<10+1), "jev-1.13.0")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want body cap error", err)
	}
}

func TestCallBodyCapErrorsNotTruncates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 11))
	}))
	defer srv.Close()
	c := JevClient{Endpoint: srv.URL, APIKey: "k", Model: "jev-test", Timeout: time.Second, BodyCap: 10}
	_, err := c.Call(context.Background(), map[string]string{"prompt": "p"}, map[string]any{})
	if !errors.Is(err, ErrBodyTooLarge) || !strings.Contains(err.Error(), "exceeds 10") {
		t.Fatalf("err = %v, want ErrBodyTooLarge cap error", err)
	}
}

func TestCallBodyCapErrorCarriesHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write(bytes.Repeat([]byte("x"), 11))
	}))
	defer srv.Close()
	c := JevClient{Endpoint: srv.URL, APIKey: "k", Model: "jev-test", Timeout: time.Second, BodyCap: 10}
	res, err := c.Call(context.Background(), map[string]string{"prompt": "p"}, map[string]any{})
	var tooLarge *BodyTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v, status carrier = %#v; want ErrBodyTooLarge with HTTP 500 status", err, tooLarge)
	}
	if res == nil || res.Status != http.StatusInternalServerError || res.RetryAfter != 7*time.Second {
		t.Fatalf("result = %#v, want status and retry-after returned with body-cap error", res)
	}
}

func TestJevClientSharedClientConcurrentRace(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write(validBody("fast", 0.9, `{"fast":0.9,"balanced":0.05,"deep":0.03,"max":0.02}`))
	}))
	defer srv.Close()
	c := &JevClient{Endpoint: srv.URL, APIKey: "k", Model: "jev-1.13.0", Timeout: time.Second, BodyCap: RouterBodyCap}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Call(context.Background(), map[string]string{"prompt": "p"}, map[string]any{}); err != nil {
				t.Errorf("Call: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 64 {
		t.Fatalf("calls = %d, want 64", calls.Load())
	}
}

func TestJevClientWarmUsesSharedIdleConnection(t *testing.T) {
	var newConns atomic.Int64
	var heads atomic.Int64
	var gotPostReused atomic.Bool
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			heads.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost {
			if v := r.Context().Value(http.LocalAddrContextKey); v == nil {
				t.Fatal("missing connection context")
			}
			_, _ = w.Write(validBody("fast", 0.9, `{"fast":0.9,"balanced":0.05,"deep":0.03,"max":0.02}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	c := &JevClient{Endpoint: srv.URL + "/v1/systemone", APIKey: "k", Model: "jev-1.13.0", Timeout: time.Second, BodyCap: RouterBodyCap}
	if err := c.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if heads.Load() != 1 {
		t.Fatalf("initial Warm HEADs = %d, want exactly one warm-up HEAD", heads.Load())
	}
	ctx := httptraceReuseContext(context.Background(), &gotPostReused)
	if _, err := c.Call(ctx, map[string]string{"prompt": "p"}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if !gotPostReused.Load() {
		t.Fatal("Call did not reuse the connection warmed by Warm")
	}
	before := newConns.Load()
	headBefore := heads.Load()
	if err := c.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if heads.Load() != headBefore {
		t.Fatalf("recent Warm sent HEAD despite idle gate: before=%d after=%d", headBefore, heads.Load())
	}
	if newConns.Load() != before {
		t.Fatalf("recent Warm opened a connection: before=%d after=%d", before, newConns.Load())
	}
	c.lastUse.Store(time.Now().Add(-61 * time.Second).UnixNano())
	if err := c.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if heads.Load() != headBefore+1 {
		t.Fatalf("idle Warm HEADs = %d, want exactly one HEAD after 60s idle gate", heads.Load())
	}
}

func httptraceReuseContext(ctx context.Context, reused *atomic.Bool) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			reused.Store(info.Reused)
		},
	})
}

func TestEmbeddedQuestionsV4IdenticalToExample(t *testing.T) {
	ex, err := os.ReadFile("../../examples/questions-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(EmbeddedQuestionsBytes(), ex) {
		t.Fatal("embedded questions-v4.json differs from examples/questions-v4.json")
	}
	if _, err := EmbeddedQuestions(); err != nil {
		t.Fatalf("EmbeddedQuestions: %v", err)
	}
}
