package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type file struct {
	Ignore []entry `json:"ignore"`
}

type entry struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: check-scan-ignore PATH")
		os.Exit(2)
	}
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		fmt.Fprintf(os.Stderr, "decode scan ignore: %v\n", err)
		os.Exit(1)
	}
	now := time.Now().UTC()
	for i, e := range f.Ignore {
		if e.ID == "" || e.Reason == "" || e.Expires == "" {
			fmt.Fprintf(os.Stderr, "ignore[%d] must set id, reason and expires\n", i)
			os.Exit(1)
		}
		exp, err := time.Parse("2006-01-02", e.Expires)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ignore[%d] expires must be YYYY-MM-DD: %v\n", i, err)
			os.Exit(1)
		}
		if !exp.After(now) {
			fmt.Fprintf(os.Stderr, "ignore[%d] %s expired on %s\n", i, e.ID, e.Expires)
			os.Exit(1)
		}
	}
}
