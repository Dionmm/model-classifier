package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dionmm/model-classifier/internal/spike"
)

func defaultFile() string {
	d, err := spike.DefaultDir()
	if err != nil {
		return spike.PayloadsFile
	}
	return filepath.Join(d, spike.PayloadsFile)
}

func runStats(args []string) int {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	file := fs.String("file", defaultFile(), "payloads.jsonl to read")
	_ = fs.Parse(args)
	f, err := os.Open(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer f.Close()
	s, err := spike.Summarise(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	spike.WriteReport(os.Stdout, s)
	return 0
}
