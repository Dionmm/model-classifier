package spike

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeJev charges 100 tokens for the questions plus 1 token per 4 prompt
// chars, and rate-limits the first request whose prompt is "retry".
func fakeJev(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	var limited atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer k" {
			t.Errorf("Authorization = %q", got)
		}
		var req struct {
			State     map[string]string `json:"state"`
			Model     string            `json:"model"`
			Questions map[string]any    `json:"questions"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil || req.Model != "jev-test" || len(req.Questions) != 3 {
			t.Errorf("bad request: %v %+v", err, req)
		}
		if req.State["prompt"] == "retry" && !limited.Swap(true) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		tokens := 100 + len(req.State["prompt"])/4
		fmt.Fprintf(w, `{"model":"jev-1.13.0","answers":{"tier":{"type":"choice","choice":"deep"}},"usage":{"input_tokens":%d}}`, tokens)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}
