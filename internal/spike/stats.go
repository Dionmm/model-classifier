package spike

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// JevStateTokenLimit is Jev 1.13's budget for state plus the longest question.
const JevStateTokenLimit = 32000

// Chars-per-token assumptions until `bench` measures the real ratio.
const (
	CharsPerTokenTypical      = 4.0
	CharsPerTokenConservative = 3.0
)

// ReadRecords decodes a payloads.jsonl stream. Malformed lines are counted,
// not fatal.
func ReadRecords(r io.Reader, fn func(Record)) (bad int, err error) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, rerr := br.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var rec Record
			if json.Unmarshal(line, &rec) != nil {
				bad++
			} else {
				fn(rec)
			}
		}
		if errors.Is(rerr, io.EOF) {
			return bad, nil
		}
		if rerr != nil {
			return bad, rerr
		}
	}
}

// Group aggregates records sharing source, shape and event.
type Group struct {
	Source, Shape, Event string
	Count                int
	PromptChars          []int
	DescriptionChars     []int
	ParseMicros          []int
	SubagentTypes        map[string]int
	ArgKeySets           map[string]int
	ModelPresent         int
	Models               map[string]int
	ArgsWereString       int
}

// Summary is the result of Summarise.
type Summary struct {
	Groups   []*Group
	BadLines int
	Sessions int
}

// Summarise groups records for reporting.
func Summarise(r io.Reader) (*Summary, error) {
	groups := map[string]*Group{}
	sessions := map[string]bool{}
	bad, err := ReadRecords(r, func(rec Record) {
		key := rec.Source + "\x00" + rec.Shape + "\x00" + rec.Event
		g := groups[key]
		if g == nil {
			g = &Group{Source: rec.Source, Shape: rec.Shape, Event: rec.Event,
				SubagentTypes: map[string]int{}, ArgKeySets: map[string]int{}, Models: map[string]int{}}
			groups[key] = g
		}
		g.Count++
		g.ParseMicros = append(g.ParseMicros, int(rec.ParseMicro))
		if rec.SessionID != "" {
			sessions[rec.SessionID] = true
		}
		d := rec.Derived
		if d == nil {
			return
		}
		g.PromptChars = append(g.PromptChars, d.PromptChars)
		g.DescriptionChars = append(g.DescriptionChars, d.DescriptionChars)
		g.SubagentTypes[orNone(d.SubagentType)]++
		g.ArgKeySets[strings.Join(d.ArgKeys, ",")]++
		if d.ModelPresent {
			g.ModelPresent++
			g.Models[orNone(d.Model)]++
		}
		if d.ArgsWasString {
			g.ArgsWereString++
		}
	})
	if err != nil {
		return nil, err
	}
	s := &Summary{BadLines: bad, Sessions: len(sessions)}
	for _, g := range groups {
		s.Groups = append(s.Groups, g)
	}
	sort.Slice(s.Groups, func(i, j int) bool {
		a, b := s.Groups[i], s.Groups[j]
		return a.Source+a.Shape+a.Event < b.Source+b.Shape+b.Event
	})
	return s, nil
}

// Percentile returns the nearest-rank percentile of vals (0 for empty input).
func Percentile(vals []int, p float64) int {
	if len(vals) == 0 {
		return 0
	}
	s := append([]int(nil), vals...)
	sort.Ints(s)
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	if rank < 1 {
		rank = 1
	}
	return s[rank-1]
}

// EstTokens estimates tokens from characters at a given chars-per-token ratio.
func EstTokens(chars int, charsPerToken float64) int {
	return int(math.Ceil(float64(chars) / charsPerToken))
}

// WriteReport prints a human-readable summary.
func WriteReport(w io.Writer, s *Summary) {
	fmt.Fprintf(w, "sessions: %d   malformed lines: %d\n", s.Sessions, s.BadLines)
	for _, g := range s.Groups {
		fmt.Fprintf(w, "\n== source=%s shape=%s event=%s  (n=%d)\n", orNone(g.Source), g.Shape, orNone(g.Event), g.Count)
		fmt.Fprintf(w, "hook parse time us   p50=%d p99=%d max=%d\n",
			Percentile(g.ParseMicros, 50), Percentile(g.ParseMicros, 99), Percentile(g.ParseMicros, 100))
		if len(g.PromptChars) == 0 {
			continue
		}
		fmt.Fprintf(w, "prompt chars         p50=%d p90=%d p99=%d max=%d\n",
			Percentile(g.PromptChars, 50), Percentile(g.PromptChars, 90), Percentile(g.PromptChars, 99), Percentile(g.PromptChars, 100))
		fmt.Fprintf(w, "description chars    p50=%d max=%d\n",
			Percentile(g.DescriptionChars, 50), Percentile(g.DescriptionChars, 100))
		over4, over3 := 0, 0
		for i, c := range g.PromptChars {
			total := c + g.DescriptionChars[i]
			if EstTokens(total, CharsPerTokenTypical) > JevStateTokenLimit {
				over4++
			}
			if EstTokens(total, CharsPerTokenConservative) > JevStateTokenLimit {
				over3++
			}
		}
		fmt.Fprintf(w, "over %dk-token Jev limit (est.)  at 4 chars/token: %d   at 3 chars/token: %d\n",
			JevStateTokenLimit/1000, over4, over3)
		fmt.Fprintf(w, "model field present: %d/%d  %s\n", g.ModelPresent, len(g.PromptChars), formatCounts(g.Models))
		if g.ArgsWereString > 0 {
			fmt.Fprintf(w, "args sent as JSON string: %d\n", g.ArgsWereString)
		}
		fmt.Fprintf(w, "subagent types: %s\n", formatCounts(g.SubagentTypes))
		fmt.Fprintf(w, "arg key sets:   %s\n", formatCounts(g.ArgKeySets))
	}
}

func formatCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
