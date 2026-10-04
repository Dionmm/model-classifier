package router

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Dionmm/model-classifier/internal/jev"
	"github.com/Dionmm/model-classifier/internal/payload"
)

const Budget = 78700

type Config struct {
	Thresholds Thresholds               `json:"thresholds"`
	Harnesses  map[string]HarnessConfig `json:"harnesses"`
}

type Thresholds map[string]float64

type HarnessConfig struct {
	Enabled      bool              `json:"enabled"`
	InputAliases map[string]string `json:"input_aliases"`
	Output       map[string]string `json:"output"`
	NoModel      NoModelConfig     `json:"no_model"`
}

type NoModelConfig struct {
	AgentTypes []string `json:"agent_types"`
	Threshold  float64  `json:"threshold"`
}

func DefaultConfig() Config {
	return Config{
		Thresholds: Thresholds{"default": 0.90},
		Harnesses: map[string]HarnessConfig{
			payload.ShapeClaude: {
				InputAliases: map[string]string{"haiku": "fast", "sonnet": "balanced", "opus": "deep", "fable": "max"},
				Output:       map[string]string{"fast": "haiku", "balanced": "sonnet", "deep": "opus", "max": "fable"},
				NoModel:      NoModelConfig{Threshold: 0.90},
			},
			payload.ShapeCopilot: {
				InputAliases: map[string]string{
					"claude-haiku-4.5": "fast",
					"claude-sonnet-5":  "balanced",
					"claude-opus-5.5":  "deep",
					"claude-fable-5.1": "max",
					"claude-fable-5":   "max",
				},
				Output:  map[string]string{"fast": "claude-haiku-4.5", "balanced": "claude-sonnet-5", "deep": "claude-opus-5.5", "max": "claude-fable-5.1"},
				NoModel: NoModelConfig{Threshold: 0.90},
			},
		},
	}
}

func (c Config) Validate() error {
	for k := range c.Thresholds {
		if !validThresholdKey(k) {
			return fmt.Errorf("threshold key %q is not default/from->to/*->to", k)
		}
	}
	for name, h := range c.Harnesses {
		for alias, tier := range h.InputAliases {
			if !isTier(tier) {
				return fmt.Errorf("%s input alias %q has unknown tier %q", name, alias, tier)
			}
		}
		for _, tier := range jev.Tiers {
			if h.Output[tier] == "" {
				return fmt.Errorf("%s output missing tier %q", name, tier)
			}
		}
		for tier := range h.Output {
			if !isTier(tier) {
				return fmt.Errorf("%s output has unknown tier %q", name, tier)
			}
		}
	}
	return nil
}

func validThresholdKey(k string) bool {
	if k == "default" {
		return true
	}
	parts := strings.Split(k, "->")
	if len(parts) != 2 {
		return false
	}
	return (parts[0] == "*" || isTier(parts[0])) && isTier(parts[1])
}

type State struct {
	SubagentType string `json:"subagent_type"`
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
}

func BuildState(subagentType, description, prompt string) (State, json.RawMessage, int, error) {
	st := State{SubagentType: subagentType, Description: description, Prompt: prompt}
	b, err := json.Marshal(st)
	if err != nil {
		return State{}, nil, 0, err
	}
	return st, json.RawMessage(b), len(b), nil
}

func StateBytes(state State) (int, error) {
	_, _, n, err := BuildState(state.SubagentType, state.Description, state.Prompt)
	return n, err
}

func OverBudget(n int) bool { return n > Budget }

type PrecheckResult struct {
	OK       bool
	Reason   string
	FromTier string
	Fill     bool
}

func Precheck(h HarnessConfig, model payload.ModelField, subagentType string) PrecheckResult {
	switch model.Kind {
	case payload.ModelAbsent:
		if slices.Contains(h.NoModel.AgentTypes, subagentType) {
			return PrecheckResult{OK: true, Fill: true}
		}
		return PrecheckResult{Reason: "no_model_skipped"}
	case payload.ModelString:
		tier, ok := h.InputAliases[model.Value]
		if !ok {
			return PrecheckResult{Reason: "unmapped_model"}
		}
		return PrecheckResult{OK: true, FromTier: tier}
	default:
		return PrecheckResult{Reason: "unmapped_model"}
	}
}

type Decision struct {
	Override  bool
	Fill      bool
	FromTier  string
	ToTier    string
	OutModel  string
	Reason    string
	Threshold float64
}

func Decide(h HarnessConfig, thresholds Thresholds, model payload.ModelField, subagentType string, answer jev.Answer) Decision {
	pc := Precheck(h, model, subagentType)
	if !pc.OK {
		return Decision{Reason: pc.Reason}
	}
	out := h.Output[answer.Choice]
	if out == "" {
		return Decision{Fill: pc.Fill, FromTier: pc.FromTier, ToTier: answer.Choice, Reason: "no_output_model"}
	}
	if pc.Fill {
		th := thresholds.Lookup("*", answer.Choice)
		d := Decision{Fill: true, ToTier: answer.Choice, OutModel: out, Threshold: max(th, h.NoModel.Threshold)}
		if answer.Confidence >= h.NoModel.Threshold && answer.Confidence >= th {
			d.Override, d.Reason = true, "fill"
		} else {
			d.Reason = "below_threshold"
		}
		return d
	}
	d := Decision{FromTier: pc.FromTier, ToTier: answer.Choice, OutModel: out}
	if answer.Choice == pc.FromTier {
		d.Reason = "agree"
		return d
	}
	th := thresholds.Lookup(pc.FromTier, answer.Choice)
	d.Threshold = th
	if answer.Confidence >= th {
		d.Override, d.Reason = true, "override"
	} else {
		d.Reason = "below_threshold"
	}
	return d
}

func (t Thresholds) Lookup(from, to string) float64 {
	if t == nil {
		return 0.90
	}
	if from != "" {
		if v, ok := t[from+"->"+to]; ok {
			return v
		}
	}
	if v, ok := t["*->"+to]; ok {
		return v
	}
	if v, ok := t["default"]; ok {
		return v
	}
	return 0.90
}

func BuildOutput(shape string, args map[string]json.RawMessage, model string) ([]byte, error) {
	updated, err := payload.SetModel(args, model)
	if err != nil {
		return nil, err
	}
	switch shape {
	case payload.ShapeClaude:
		return []byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":` + string(updated) + `}}`), nil
	case payload.ShapeCopilot:
		return []byte(`{"modifiedArgs":` + string(updated) + `}`), nil
	default:
		return nil, fmt.Errorf("unknown shape %q", shape)
	}
}

func isTier(t string) bool { return slices.Contains(jev.Tiers, t) }
