package spike

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestModelTier(t *testing.T) {
	for in, want := range map[string]string{
		"haiku": "fast", "claude-haiku-4-5-20251001": "fast",
		"sonnet": "balanced", "claude-sonnet-5": "balanced",
		"opus": "deep", "fable": "max", "claude-fable-5.1": "max", "claude-fable-5": "max",
		"": "", "gpt-5.5": "",
	} {
		if got := ModelTier(in); got != want {
			t.Errorf("ModelTier(%q) = %q, want %q", in, got, want)
		}
	}
}

func tierRes(mode, model, choice string, conf float64) BenchResult {
	a, _ := json.Marshal(map[string]any{"type": "choice", "choice": choice, "confidence": conf})
	return BenchResult{Mode: mode, Status: 200, OrchestratorModel: model, Description: model + "-task",
		Answers: map[string]json.RawMessage{"tier": a}}
}

func TestWriteAgreement(t *testing.T) {
	rs := []BenchResult{
		tierRes("fresh", "opus", "deep", 0.95),               // >=0.90 agree
		tierRes("fresh", "sonnet", "deep", 0.92),             // >=0.90 disagree
		tierRes("fresh", "claude-sonnet-5", "balanced", 0.8), // mid agree
		tierRes("fresh", "fable", "fast", 0.5),               // low disagree
		tierRes("fresh", "", "deep", 0.99),                   // no model
		tierRes("fresh", "gpt-5.5", "deep", 0.99),            // unmapped
		tierRes("reuse", "opus", "fast", 0.99),               // other mode: ignored
	}
	var b bytes.Buffer
	WriteAgreement(&b, rs, "fresh")
	out := b.String()
	for _, want := range []string{
		"answered=6, no model set=1, unmapped model=1",
		">=0.90       2       1         1   50.0",
		"0.70-0.90    1       1         0  100.0",
		"<0.70        1       0         1    0.0",
		"orchestrator \\ jev     fast balanced     deep      max",
		"balanced                  0        1        1        0", // sonnet: 1 balanced, 1 deep
		"deep                      0        0        1        0", // opus->deep
		"max                       1        0        0        0", // fable->fast
		`#0 sonnet(balanced) -> jev deep 0.92  "sonnet-task"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "fable(max) -> jev fast") {
		t.Errorf("low-confidence disagreement listed as override candidate:\n%s", out)
	}
}

func TestOrchestratorModelNotSentToJev(t *testing.T) {
	st := BenchState(map[string]any{"prompt": "p", "model": "opus"})
	if _, ok := st["model"]; ok {
		t.Fatalf("orchestrator model leaked into Jev state: %v", st)
	}
}

func TestCollectSpawnsKeepsModelVariants(t *testing.T) {
	line := func(m string) string {
		return `{"v":1,"event":"PreToolUse","raw":{"tool_name":"Agent","tool_input":{"prompt":"same","model":"` + m + `"}}}` + "\n"
	}
	got, err := CollectSpawns(strings.NewReader(line("opus") + line("sonnet") + line("opus")))
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d spawns (err %v), want 2", len(got), err)
	}
}
