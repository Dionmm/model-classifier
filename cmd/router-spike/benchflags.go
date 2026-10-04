package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/Dionmm/model-classifier/internal/spike"
)

type benchFlags struct {
	file, modes, model, endpoint, scheme, out, questions string
	limit, rpm                                           int
	timeout                                              time.Duration
}

func parseBenchFlags(args []string) benchFlags {
	var b benchFlags
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	fs.StringVar(&b.file, "file", defaultFile(), "payloads.jsonl to read")
	fs.IntVar(&b.limit, "limit", 50, "max distinct spawns to send (0 = all)")
	fs.StringVar(&b.modes, "modes", "fresh,reuse", "fresh (new TLS per call, like a hook process) and/or reuse")
	fs.StringVar(&b.model, "model", "jev-latest", "Jev model or alias")
	fs.StringVar(&b.endpoint, "endpoint", spike.DefaultEndpoint, "System One endpoint")
	fs.StringVar(&b.scheme, "auth-scheme", "Bearer", "Authorization header prefix; empty sends the bare key")
	fs.IntVar(&b.rpm, "rpm", 300, "max requests per minute")
	fs.DurationVar(&b.timeout, "timeout", 30*time.Second, "per-request timeout")
	fs.StringVar(&b.out, "out", "bench-results.jsonl", "JSONL results file")
	fs.StringVar(&b.questions, "questions", "", "JSON file of Jev questions (default: built-in placeholder set)")
	_ = fs.Parse(args)
	return b
}

func parseModes(s string) ([]string, error) {
	var ms []string
	for _, m := range strings.Split(s, ",") {
		if m = strings.TrimSpace(m); m != "fresh" && m != "reuse" {
			return nil, fmt.Errorf("unknown mode %q", m)
		}
		ms = append(ms, m)
	}
	return ms, nil
}
