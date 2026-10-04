package router

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dionmm/model-classifier/internal/jev"
	"github.com/Dionmm/model-classifier/internal/payload"
)

func answer(tier string, conf float64) jev.Answer {
	return jev.Answer{Choice: tier, Confidence: conf, Probabilities: map[string]float64{
		"fast": 0.25, "balanced": 0.25, "deep": 0.25, "max": 0.25,
	}}
}

func TestDefaultConfigAndValidation(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig invalid: %v", err)
	}
	if cfg.Harnesses[payload.ShapeClaude].Enabled || cfg.Harnesses[payload.ShapeCopilot].Enabled {
		t.Fatal("harnesses must default to disabled")
	}
	if got := cfg.Harnesses[payload.ShapeCopilot].InputAliases["claude-fable-5"]; got != "max" {
		t.Fatalf("fable-5 alias = %q, want max", got)
	}
	for name, mutate := range map[string]func(Config){
		"bad threshold key": func(c Config) { c.Thresholds["fast=>deep"] = 0.9 },
		"bad input tier": func(c Config) {
			c.Harnesses["claude"] = HarnessConfig{InputAliases: map[string]string{"x": "huge"}, Output: c.Harnesses["claude"].Output}
		},
		"bad output tier": func(c Config) {
			c.Harnesses["claude"] = HarnessConfig{InputAliases: c.Harnesses["claude"].InputAliases, Output: map[string]string{"fast": "h", "balanced": "s", "deep": "o", "max": "f", "huge": "x"}}
		},
		"missing output": func(c Config) {
			c.Harnesses["claude"] = HarnessConfig{InputAliases: c.Harnesses["claude"].InputAliases, Output: map[string]string{"fast": "h", "balanced": "s", "deep": "o"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := DefaultConfig()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatal("Validate accepted invalid config")
			}
		})
	}
}

func TestDecisionTableThresholds(t *testing.T) {
	h := DefaultConfig().Harnesses[payload.ShapeClaude]
	for _, from := range jev.Tiers {
		for _, to := range jev.Tiers {
			name := from + "->" + to
			for _, tc := range []struct {
				suffix string
				conf   float64
				want   bool
			}{
				{"below", 0.66, false},
				{"at", 0.67, from != to},
				{"above", 0.68, from != to},
			} {
				t.Run(name+" "+tc.suffix, func(t *testing.T) {
					d := Decide(h, Thresholds{"default": 0.99, name: 0.67}, payload.ModelField{Kind: payload.ModelString, Value: h.Output[from]}, "", answer(to, tc.conf))
					if d.Override != tc.want {
						t.Fatalf("override = %v, want %v (>= threshold inclusive for %s)", d.Override, tc.want, name)
					}
					if from == to && d.Reason != "agree" {
						t.Fatalf("reason = %q, want agree", d.Reason)
					}
				})
			}
		}
	}
	t.Run("star threshold beats default", func(t *testing.T) {
		d := Decide(h, Thresholds{"default": 0.99, "*->deep": 0.67}, payload.ModelField{Kind: payload.ModelString, Value: "haiku"}, "", answer("deep", 0.67))
		if !d.Override || d.Threshold != 0.67 {
			t.Fatalf("decision = %+v, want *->to inclusive override", d)
		}
	})
	t.Run("specific from-to beats star-to", func(t *testing.T) {
		d := Decide(h, Thresholds{"default": 0.50, "fast->deep": 0.95, "*->deep": 0.60}, payload.ModelField{Kind: payload.ModelString, Value: "haiku"}, "", answer("deep", 0.70))
		if d.Override || d.Threshold != 0.95 || d.Reason != "below_threshold" {
			t.Fatalf("decision = %+v, want fast->deep to beat *->deep and block override", d)
		}
	})
	t.Run("star threshold beats default inverse", func(t *testing.T) {
		d := Decide(h, Thresholds{"default": 0.95, "*->deep": 0.60}, payload.ModelField{Kind: payload.ModelString, Value: "haiku"}, "", answer("deep", 0.70))
		if !d.Override || d.Threshold != 0.60 {
			t.Fatalf("decision = %+v, want *->deep to beat default and override", d)
		}
	})
	t.Run("threshold above one is off", func(t *testing.T) {
		d := Decide(h, Thresholds{"default": 0.90, "fast->deep": 1.01}, payload.ModelField{Kind: payload.ModelString, Value: "haiku"}, "", answer("deep", 1.0))
		if d.Override || d.Reason != "below_threshold" {
			t.Fatalf("decision = %+v, want off direction", d)
		}
	})
}

func TestPrecheckModelTriState(t *testing.T) {
	h := DefaultConfig().Harnesses[payload.ShapeCopilot]
	h.NoModel.AgentTypes = []string{"research"}
	for name, tc := range map[string]struct {
		model      payload.ModelField
		agentType  string
		ok         bool
		from       string
		fill       bool
		wantReason string
	}{
		"absent allowed":      {payload.ModelField{Kind: payload.ModelAbsent}, "research", true, "", true, ""},
		"absent not allowed":  {payload.ModelField{Kind: payload.ModelAbsent}, "general-purpose", false, "", false, "no_model_skipped"},
		"empty invalid":       {payload.ModelField{Kind: payload.ModelInvalid}, "", false, "", false, "unmapped_model"},
		"non-string invalid":  {payload.ModelField{Kind: payload.ModelInvalid}, "", false, "", false, "unmapped_model"},
		"unmapped contains":   {payload.ModelField{Kind: payload.ModelString, Value: "claude-opus-5.5-preview"}, "", false, "", false, "unmapped_model"},
		"unmapped tier word":  {payload.ModelField{Kind: payload.ModelString, Value: "super-haiku"}, "", false, "", false, "unmapped_model"},
		"exact mapped string": {payload.ModelField{Kind: payload.ModelString, Value: "claude-opus-5.5"}, "", true, "deep", false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := Precheck(h, tc.model, tc.agentType)
			if got.OK != tc.ok || got.FromTier != tc.from || got.Fill != tc.fill || got.Reason != tc.wantReason {
				t.Fatalf("Precheck = %+v", got)
			}
		})
	}
}

func TestFillDecisionAllowedAndThresholds(t *testing.T) {
	h := DefaultConfig().Harnesses[payload.ShapeCopilot]
	h.NoModel.AgentTypes = []string{"research"}
	h.NoModel.Threshold = 0.80
	model := payload.ModelField{Kind: payload.ModelAbsent}
	for name, conf := range map[string]float64{"below fill threshold": 0.79, "below star threshold": 0.84, "at both thresholds": 0.85} {
		t.Run(name, func(t *testing.T) {
			d := Decide(h, Thresholds{"default": 0.90, "*->fast": 0.85}, model, "research", answer("fast", conf))
			want := conf >= 0.85
			if d.Override != want {
				t.Fatalf("decision = %+v, want fill override=%v", d, want)
			}
			if want && d.Reason != "fill" {
				t.Fatalf("reason = %q, want fill", d.Reason)
			}
		})
	}
	d := Decide(h, Thresholds{"default": 0.90}, model, "general-purpose", answer("fast", 0.99))
	if d.Override || d.Reason != "no_model_skipped" {
		t.Fatalf("not allowed decision = %+v", d)
	}
}

func TestBudgetBoundaryEscapedBytes(t *testing.T) {
	prompt := "\"\\\né"
	for {
		_, _, n, err := BuildState("research", "desc", prompt)
		if err != nil {
			t.Fatal(err)
		}
		if n == Budget {
			break
		}
		if n > Budget {
			t.Fatalf("cannot build exact budget, got %d", n)
		}
		prompt += "a"
	}
	_, raw, n, err := BuildState("research", "desc", prompt)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(raw) || n != Budget || OverBudget(n) {
		t.Fatalf("n=%d raw=%d OverBudget=%v, want exact boundary allowed", n, len(raw), OverBudget(n))
	}
	_, _, n, err = BuildState("research", "desc", prompt+"a")
	if err != nil {
		t.Fatal(err)
	}
	if n != Budget+1 || !OverBudget(n) {
		t.Fatalf("n=%d OverBudget=%v, want one byte over rejected", n, OverBudget(n))
	}
}

func TestBuildOutputShapesNoPermissionDecision(t *testing.T) {
	args := map[string]json.RawMessage{
		"prompt": json.RawMessage(`"<file>&</file>\u2028"`),
		"n":      json.RawMessage(`12345678901234567890`),
		"nested": json.RawMessage(`{"z": 1, "inner": {"keep": [1,  2]}}`),
	}
	for _, shape := range []string{payload.ShapeClaude, payload.ShapeCopilot} {
		t.Run(shape, func(t *testing.T) {
			got, err := BuildOutput(shape, args, "new-model")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(got), "permissionDecision") {
				t.Fatalf("output contains permissionDecision: %s", got)
			}
			if strings.HasSuffix(string(got), "\n") {
				t.Fatalf("output has trailing newline: %q", got)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(got, &top); err != nil {
				t.Fatal(err)
			}
			switch shape {
			case payload.ShapeClaude:
				var out struct {
					HookSpecificOutput struct {
						HookEventName string                     `json:"hookEventName"`
						UpdatedInput  map[string]json.RawMessage `json:"updatedInput"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(got, &out); err != nil {
					t.Fatal(err)
				}
				if len(top) != 1 || out.HookSpecificOutput.HookEventName != "PreToolUse" || string(out.HookSpecificOutput.UpdatedInput["model"]) != `"new-model"` {
					t.Fatalf("claude output = %s", got)
				}
				if string(out.HookSpecificOutput.UpdatedInput["prompt"]) != string(args["prompt"]) || string(out.HookSpecificOutput.UpdatedInput["nested"]) != string(args["nested"]) {
					t.Fatalf("claude raw values changed: %s", got)
				}
			case payload.ShapeCopilot:
				var out struct {
					ModifiedArgs map[string]json.RawMessage `json:"modifiedArgs"`
				}
				if err := json.Unmarshal(got, &out); err != nil {
					t.Fatal(err)
				}
				if len(top) != 1 || string(out.ModifiedArgs["model"]) != `"new-model"` || string(out.ModifiedArgs["n"]) != `12345678901234567890` {
					t.Fatalf("copilot output = %s", got)
				}
				if string(out.ModifiedArgs["prompt"]) != string(args["prompt"]) || string(out.ModifiedArgs["nested"]) != string(args["nested"]) {
					t.Fatalf("copilot raw values changed: %s", got)
				}
			}
		})
	}
}

func TestNoOutputModelPreventsOverride(t *testing.T) {
	h := DefaultConfig().Harnesses[payload.ShapeClaude]
	delete(h.Output, "deep")
	d := Decide(h, Thresholds{"default": 0.9}, payload.ModelField{Kind: payload.ModelString, Value: "haiku"}, "", answer("deep", 1))
	if d.Override || d.Reason != "no_output_model" {
		t.Fatalf("decision = %+v, want no_output_model fail-open", d)
	}
}
