package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanDir(t *testing.T) {
	cases := map[string][]string{
		"/a": {"--nope", "--dir", "/a"},
		"/b": {"-dir=/b", "extra"},
		"/c": {"--log-all", "true", "--dir=/c"},
		"":   {"--source", "x", "--", "--dir", "/ignored"},
	}
	for want, args := range cases {
		if got := scanDir(args); got != want {
			t.Errorf("scanDir(%q) = %q, want %q", args, got, want)
		}
	}
}

// Reproduces the review finding: --dir after a bad flag or stray word was
// ignored and records went to the default directory.
func TestHookHonoursDirAfterParseStops(t *testing.T) {
	spawn := `{"tool_name":"Agent","hook_event_name":"PreToolUse","tool_input":{"prompt":"p"}}`
	for name, pre := range map[string][]string{
		"bad flag":   {"--nope"},
		"bool value": {"--log-all", "true"},
		"stray word": {"--source", "x", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, sandbox := t.TempDir(), t.TempDir()
			cmd := exec.Command(bin, append(append([]string{"hook"}, pre...), "--dir", dir)...)
			cmd.Stdin = strings.NewReader(spawn)
			cmd.Env = append(os.Environ(), "MODEL_ROUTER_SPIKE_DIR="+sandbox)
			out, err := cmd.CombinedOutput()
			if err != nil || len(out) != 0 {
				t.Fatalf("err=%v output=%q", err, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "payloads.jsonl")); err != nil {
				t.Errorf("record not written to --dir: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "errors.log")); err != nil {
				t.Errorf("flag problem not logged to --dir: %v", err)
			}
			if entries, _ := os.ReadDir(sandbox); len(entries) != 0 {
				t.Errorf("wrote to default dir: %v", entries)
			}
		})
	}
}
