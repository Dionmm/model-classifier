package spike

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Dionmm/model-classifier/internal/jev"
)

// Tiers in report order.
var Tiers = jev.Tiers

// ModelTier maps an orchestrator-chosen model name to a tier. The mapping is
// a proposal: haiku=fast, sonnet=balanced, opus=deep, fable=max. Unknown or
// empty names return "".
func ModelTier(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "haiku"):
		return "fast"
	case strings.Contains(m, "sonnet"):
		return "balanced"
	case strings.Contains(m, "opus"):
		return "deep"
	case strings.Contains(m, "fable"):
		return "max"
	}
	return ""
}

// TierAnswer decodes the tier Choice answer.
func TierAnswer(answers map[string]json.RawMessage) (choice string, confidence float64, ok bool) {
	var a struct {
		Choice     string   `json:"choice"`
		Confidence *float64 `json:"confidence"`
	}
	if json.Unmarshal(answers["tier"], &a) != nil || a.Choice == "" {
		return "", 0, false
	}
	if a.Confidence != nil {
		confidence = *a.Confidence
	}
	return a.Choice, confidence, true
}

type band struct {
	label string
	min   float64
}

// Bands are checked in order; the first whose min the confidence meets wins.
var agreementBands = []band{{">=0.90", 0.9}, {"0.70-0.90", 0.7}, {"<0.70", -1}}

// WriteAgreement compares Jev's tier with the orchestrator's model for one
// mode's results (so a spawn is not counted once per mode).
func WriteAgreement(w io.Writer, results []BenchResult, mode string) {
	type cell struct{ n, agree int }
	bands := map[string]*cell{}
	matrix := map[[2]string]int{}
	var noModel, unmapped, answered int
	var highDisagree []string
	for _, r := range results {
		if r.Mode != mode || r.Status != 200 {
			continue
		}
		jt, conf, ok := TierAnswer(r.Answers)
		if !ok {
			continue
		}
		answered++
		if r.OrchestratorModel == "" {
			noModel++
			continue
		}
		ot := ModelTier(r.OrchestratorModel)
		if ot == "" {
			unmapped++
			continue
		}
		matrix[[2]string{ot, jt}]++
		for _, b := range agreementBands {
			if conf >= b.min {
				c := bands[b.label]
				if c == nil {
					c = &cell{}
					bands[b.label] = c
				}
				c.n++
				if ot == jt {
					c.agree++
				} else if b.min >= 0.9 {
					highDisagree = append(highDisagree, fmt.Sprintf("  #%d %s(%s) -> jev %s %.2f  %q",
						r.Index, r.OrchestratorModel, ot, jt, conf, r.Description))
				}
				break
			}
		}
	}
	fmt.Fprintf(w, "\n== agreement (mode=%s, answered=%d, no model set=%d, unmapped model=%d)\n",
		mode, answered, noModel, unmapped)
	fmt.Fprintln(w, "confidence   n   agree  disagree  agree%")
	for _, b := range agreementBands {
		c := bands[b.label]
		if c == nil {
			c = &cell{}
		}
		pct := 0.0
		if c.n > 0 {
			pct = 100 * float64(c.agree) / float64(c.n)
		}
		fmt.Fprintf(w, "%-10s %3d  %6d  %8d  %5.1f\n", b.label, c.n, c.agree, c.n-c.agree, pct)
	}
	fmt.Fprint(w, "orchestrator \\ jev")
	for _, j := range Tiers {
		fmt.Fprintf(w, " %8s", j)
	}
	fmt.Fprintln(w)
	for _, o := range Tiers {
		fmt.Fprintf(w, "%-18s", o)
		for _, j := range Tiers {
			fmt.Fprintf(w, " %8d", matrix[[2]string{o, j}])
		}
		fmt.Fprintln(w)
	}
	if len(highDisagree) > 0 {
		fmt.Fprintln(w, "high-confidence disagreements (override candidates):")
		for _, s := range highDisagree {
			fmt.Fprintln(w, s)
		}
	}
}
