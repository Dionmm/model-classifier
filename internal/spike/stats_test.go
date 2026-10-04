package spike

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestPercentileNearestRank(t *testing.T) {
	v := []int{5, 1, 4, 2, 3}
	for p, want := range map[float64]int{0: 1, 50: 3, 90: 5, 100: 5} {
		if got := Percentile(v, p); got != want {
			t.Errorf("p%v = %d, want %d", p, got, want)
		}
	}
	if Percentile(nil, 50) != 0 {
		t.Error("empty input")
	}
	if v[0] != 5 {
		t.Error("Percentile mutated its input")
	}
}

func spawnLine(source, event string, promptChars int) string {
	return fmt.Sprintf(`{"v":1,"source":%q,"event":%q,"shape":"claude","session_id":"s","derived":{"arg_keys":["prompt"],"prompt_chars":%d,"subagent_type":"Explore"}}`,
		source, event, promptChars)
}

func TestSummariseCountsOverLimitAtBothRatios(t *testing.T) {
	// 100k chars: 25k tokens at 4 c/t (under), 33.4k at 3 c/t (over).
	// 200k chars: over at both.
	in := strings.Join([]string{
		spawnLine("claude", "PreToolUse", 1000),
		spawnLine("claude", "PreToolUse", 100000),
		spawnLine("claude", "PreToolUse", 200000),
		"not json",
	}, "\n")
	s, err := Summarise(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if s.BadLines != 1 || len(s.Groups) != 1 || s.Groups[0].Count != 3 {
		t.Fatalf("summary: bad=%d groups=%d", s.BadLines, len(s.Groups))
	}
	var out bytes.Buffer
	WriteReport(&out, s)
	if !strings.Contains(out.String(), "at 4 chars/token: 1   at 3 chars/token: 2") {
		t.Errorf("over-limit counts wrong:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "subagent types: Explore=3") {
		t.Errorf("subagent types missing:\n%s", out.String())
	}
}
