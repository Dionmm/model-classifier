package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBenchBadQuestionsKeepsResults(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "results.jsonl")
	bad := filepath.Join(dir, "q.json")
	if err := os.WriteFile(out, []byte("previous run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(`{"tier":{"type":"noul"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "k")
	payloads := filepath.Join(dir, "payloads.jsonl")
	if err := os.WriteFile(payloads, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code := runBench([]string{"--questions", bad, "--out", out,
		"--file", payloads, "--endpoint", "http://127.0.0.1:1", "--timeout", "1s"})
	if code != 2 {
		t.Errorf("exit %d, want 2 for invalid questions", code)
	}
	if b, _ := os.ReadFile(out); string(b) != "previous run\n" {
		t.Errorf("results file changed to %q", b)
	}
}
