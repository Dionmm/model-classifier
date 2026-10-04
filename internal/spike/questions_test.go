package spike

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func loadExample(t *testing.T, name string) map[string]any {
	t.Helper()
	f, err := os.Open("../../examples/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	q, err := LoadQuestions(f)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return q
}

func TestExampleQuestionsLoad(t *testing.T) {
	v1 := loadExample(t, "questions-v1.json")
	loadExample(t, "questions-v2.json")
	for _, name := range []string{"questions-v3.json", "questions-v4.json"} {
		var keys []string
		for k := range loadExample(t, name)["tier"].(map[string]any)["criteria"].(map[string]any) {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if want := slices.Sorted(slices.Values(Tiers)); !slices.Equal(keys, want) {
			t.Errorf("%s tier options = %v, want %v", name, keys, want)
		}
	}
	var builtin map[string]any
	b, _ := json.Marshal(BenchQuestions())
	_ = json.Unmarshal(b, &builtin)
	if !reflect.DeepEqual(v1, builtin) {
		t.Errorf("examples/questions-v1.json has drifted from BenchQuestions()")
	}
}

func TestLoadQuestionsRejects(t *testing.T) {
	for name, in := range map[string]string{
		"bad json":        `{`,
		"no tier":         `{"x":{"type":"noul","instructions":"i"}}`,
		"tier not choice": `{"tier":{"type":"score","criteria":{"fast":"a","deep":"b"}}}`,
		"one option":      `{"tier":{"type":"choice","criteria":{"deep":"b"}}}`,
		"unknown option":  `{"tier":{"type":"choice","criteria":{"fast":"a","huge":"b"}}}`,
	} {
		if _, err := LoadQuestions(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRunBenchSendsCustomQuestions(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Questions map[string]struct {
				Instructions string `json:"instructions"`
			} `json:"questions"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		seen = append(seen, req.Questions["tier"].Instructions)
		mu.Unlock()
		io.WriteString(w, `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":10}}`)
	}))
	defer srv.Close()
	q := map[string]any{"tier": map[string]any{"type": "choice", "instructions": "CUSTOM",
		"criteria": map[string]any{"fast": "a", "deep": "b"}}}
	RunBench(context.Background(), []map[string]any{{"prompt": "p"}}, BenchOptions{
		Client:    &JevClient{Endpoint: srv.URL, APIKey: "k", Model: "m", Timeout: 5 * time.Second},
		Modes:     []string{"reuse"},
		Out:       io.Discard,
		Sleep:     func(time.Duration) {},
		Questions: q,
	})
	if len(seen) != 2 || seen[0] != "CUSTOM" || seen[1] != "CUSTOM" {
		t.Errorf("baseline and spawn calls sent tier instructions %q, want CUSTOM twice", seen)
	}
}
