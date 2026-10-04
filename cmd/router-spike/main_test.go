package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "router-spike-test")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "router-spike")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// The hook must exit 0 with empty stdout and stderr on every path: Copilot
// denies a spawn when a command preToolUse hook exits non-zero.
func TestHookAlwaysSilentAndExitsZero(t *testing.T) {
	logDir, sandbox := t.TempDir(), t.TempDir()
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	spawn := `{"tool_name":"Agent","hook_event_name":"PreToolUse","tool_input":{"prompt":"p"}}`
	cases := []struct {
		name, stdin string
		args        []string
	}{
		{"spawn", spawn, []string{"--source", "claude", "--dir", logDir}},
		{"garbage", "not json at all", []string{"--dir", logDir}},
		{"empty", "", []string{"--dir", logDir}},
		{"bad flag", spawn, []string{"--dir", logDir, "--nope"}},
		{"unwritable dir", spawn, []string{"--dir", notADir}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(bin, append([]string{"hook"}, c.args...)...)
			cmd.Stdin = strings.NewReader(c.stdin)
			cmd.Env = append(os.Environ(), "MODEL_ROUTER_SPIKE_DIR="+sandbox)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("exit: %v (stderr %q)", err, stderr.String())
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("stdout=%q stderr=%q, want both empty", stdout.String(), stderr.String())
			}
		})
	}
	b, err := os.ReadFile(filepath.Join(logDir, "payloads.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// "spawn" and "bad flag" both carry a well-formed spawn; the rest don't.
	if n := bytes.Count(b, []byte("\n")); n != 2 {
		t.Errorf("logged %d records, want 2", n)
	}
	if entries, _ := os.ReadDir(sandbox); len(entries) != 0 {
		t.Errorf("hook wrote to the default dir despite --dir: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(logDir, "errors.log")); err != nil {
		t.Errorf("bad flag should be recorded in errors.log: %v", err)
	}
}
