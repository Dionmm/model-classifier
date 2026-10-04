package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Dionmm/model-classifier/internal/spike"
)

func runBench(args []string) int {
	b := parseBenchFlags(args)
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "TYPESAFE_API_KEY is not set")
		return 1
	}
	modes, err := parseModes(b.modes)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var questions map[string]any
	if b.questions != "" {
		qf, err := os.Open(b.questions)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		questions, err = spike.LoadQuestions(qf)
		qf.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	in, err := os.Open(b.file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	spawns, err := spike.CollectSpawns(in)
	in.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if b.limit > 0 && len(spawns) > b.limit {
		spawns = spawns[:b.limit]
	}
	out, err := os.OpenFile(b.out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer out.Close()
	var interval time.Duration
	if b.rpm > 0 {
		interval = time.Minute / time.Duration(b.rpm)
	}
	fmt.Fprintf(os.Stderr, "sending %d spawns x %d modes (+1 baseline) to %s\n", len(spawns), len(modes), b.model)
	results := spike.RunBench(context.Background(), spawns, spike.BenchOptions{
		Client: &spike.JevClient{Endpoint: b.endpoint, APIKey: key, AuthScheme: b.scheme,
			Model: b.model, Timeout: b.timeout},
		Modes:       modes,
		MinInterval: interval,
		MaxAttempts: 3,
		Out:         out,
		Questions:   questions,
	})
	spike.WriteBenchReport(os.Stdout, results)
	spike.WriteAgreement(os.Stdout, results, modes[0])
	fmt.Fprintf(os.Stderr, "\nresults written to %s\n", b.out)
	return 0
}
