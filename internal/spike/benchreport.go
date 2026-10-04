package spike

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
)

// PercentileF is Percentile for floats.
func PercentileF(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	if rank < 1 {
		rank = 1
	}
	return s[rank-1]
}

// WriteBenchReport prints latency, token-ratio and answer summaries per mode.
func WriteBenchReport(w io.Writer, results []BenchResult) {
	byMode := map[string][]BenchResult{}
	var modes []string
	for _, r := range results {
		if _, ok := byMode[r.Mode]; !ok {
			modes = append(modes, r.Mode)
		}
		byMode[r.Mode] = append(byMode[r.Mode], r)
	}
	for _, m := range modes {
		rs := byMode[m]
		var total, ttfb, tls, cpt []float64
		statuses, tiers := map[string]int{}, map[string]int{}
		for _, r := range rs {
			key := strconv.Itoa(r.Status)
			if r.Error != "" {
				key = "error"
			}
			statuses[key]++
			if r.Status != 200 {
				continue
			}
			total, ttfb = append(total, r.Timing.TotalMs), append(ttfb, r.Timing.TTFBMs)
			if !r.Timing.Reused {
				tls = append(tls, r.Timing.TLSMs)
			}
			if r.CharsPerToken > 0 {
				cpt = append(cpt, r.CharsPerToken)
			}
			var tier struct{ Choice string }
			if json.Unmarshal(r.Answers["tier"], &tier) == nil && tier.Choice != "" {
				tiers[tier.Choice]++
			}
		}
		fmt.Fprintf(w, "\n== mode=%s  (n=%d)  status: %s\n", m, len(rs), formatCounts(statuses))
		if len(total) == 0 {
			continue
		}
		fmt.Fprintf(w, "total ms   p50=%.0f p90=%.0f p99=%.0f max=%.0f\n",
			PercentileF(total, 50), PercentileF(total, 90), PercentileF(total, 99), PercentileF(total, 100))
		fmt.Fprintf(w, "ttfb ms    p50=%.0f p90=%.0f\n", PercentileF(ttfb, 50), PercentileF(ttfb, 90))
		if len(tls) > 0 {
			fmt.Fprintf(w, "tls ms     p50=%.0f p90=%.0f  (new connections: %d)\n",
				PercentileF(tls, 50), PercentileF(tls, 90), len(tls))
		}
		if len(cpt) > 0 {
			fmt.Fprintf(w, "chars/token p10=%.2f p50=%.2f p90=%.2f\n",
				PercentileF(cpt, 10), PercentileF(cpt, 50), PercentileF(cpt, 90))
		}
		if len(tiers) > 0 {
			fmt.Fprintf(w, "tier (placeholder criteria): %s\n", formatCounts(tiers))
		}
	}
}
