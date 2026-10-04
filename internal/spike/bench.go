package spike

import (
	"context"
	"encoding/json"
	"io"
	"time"
	"unicode/utf8"
)

// BenchOptions configure a benchmark run.
type BenchOptions struct {
	Client      *JevClient // template; Fresh is set per mode
	Modes       []string   // "fresh" and/or "reuse"
	MinInterval time.Duration
	MaxAttempts int
	Out         io.Writer // JSONL results
	Sleep       func(time.Duration)
	Questions   map[string]any // nil = BenchQuestions()
}

// BenchResult is one JSONL line of bench output.
type BenchResult struct {
	Index int    `json:"index"`
	Mode  string `json:"mode"`
	// OrchestratorModel is the model the spawn asked for. It is recorded for
	// comparison only and never sent to Jev.
	OrchestratorModel string                     `json:"orchestrator_model,omitempty"`
	Description       string                     `json:"description,omitempty"`
	Attempts          int                        `json:"attempts"`
	Status            int                        `json:"status"`
	Error             string                     `json:"error,omitempty"`
	StateChars        int                        `json:"state_chars"`
	InputTokens       int                        `json:"input_tokens"`
	BaselineTokens    int                        `json:"baseline_tokens"`
	CharsPerToken     float64                    `json:"chars_per_token,omitempty"`
	JevModel          string                     `json:"jev_model,omitempty"`
	Answers           map[string]json.RawMessage `json:"answers,omitempty"`
	ErrorBody         string                     `json:"error_body,omitempty"`
	Timing            Timing                     `json:"timing"`
}

// RunBench calls Jev once per spawn per mode. A baseline call with empty
// state first measures the token cost of the questions alone, so each
// result's chars-per-token ratio covers only the state.
func RunBench(ctx context.Context, spawns []map[string]any, o BenchOptions) []BenchResult {
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.MaxAttempts < 1 {
		o.MaxAttempts = 1
	}
	if o.Questions == nil {
		o.Questions = BenchQuestions()
	}
	enc := json.NewEncoder(o.Out)
	var results []BenchResult
	emit := func(r BenchResult) {
		results = append(results, r)
		_ = enc.Encode(r)
	}
	clients := map[string]*JevClient{}
	for _, m := range o.Modes {
		c := JevClient{
			Endpoint: o.Client.Endpoint, APIKey: o.Client.APIKey, AuthScheme: o.Client.AuthScheme,
			Model: o.Client.Model, Timeout: o.Client.Timeout, BodyCap: o.Client.BodyCap,
		}
		c.Fresh = m == "fresh"
		clients[m] = &c
	}
	base := o.one(ctx, &JevClient{Endpoint: o.Client.Endpoint, APIKey: o.Client.APIKey,
		AuthScheme: o.Client.AuthScheme, Model: o.Client.Model, Timeout: o.Client.Timeout},
		BenchState(nil), -1, "baseline", 0)
	emit(base)
	for i, args := range spawns {
		for _, m := range o.Modes {
			o.Sleep(o.MinInterval)
			r := o.one(ctx, clients[m], BenchState(args), i, m, base.InputTokens)
			r.OrchestratorModel, _ = args["model"].(string)
			r.Description, _ = args["description"].(string)
			emit(r)
		}
	}
	return results
}

func (o BenchOptions) one(ctx context.Context, c *JevClient, state map[string]any, idx int, mode string, baseline int) BenchResult {
	r := BenchResult{Index: idx, Mode: mode, BaselineTokens: baseline}
	for _, k := range []string{"subagent_type", "description", "prompt"} {
		s, _ := state[k].(string)
		r.StateChars += utf8.RuneCountInString(s)
	}
	var res *CallResult
	var err error
	for r.Attempts = 1; ; r.Attempts++ {
		res, err = c.Call(ctx, state, o.Questions)
		if err != nil || r.Attempts >= o.MaxAttempts || (res.Status != 429 && res.Status != 529) {
			break
		}
		wait := res.RetryAfter
		if wait == 0 {
			wait = time.Duration(r.Attempts) * 2 * time.Second
		}
		o.Sleep(wait)
	}
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Status, r.Timing = res.Status, res.Timing
	if res.Status != 200 {
		r.ErrorBody = string(res.Body)
		return r
	}
	var jr JevResponse
	if err := json.Unmarshal(res.Body, &jr); err != nil {
		r.Error = "decode response: " + err.Error()
		return r
	}
	r.JevModel, r.Answers, r.InputTokens = jr.Model, jr.Answers, jr.Usage.InputTokens
	if baseline > 0 && r.InputTokens > baseline && r.StateChars > 0 {
		r.CharsPerToken = float64(r.StateChars) / float64(r.InputTokens-baseline)
	}
	return r
}
