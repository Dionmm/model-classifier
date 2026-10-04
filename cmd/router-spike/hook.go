package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Dionmm/model-classifier/internal/spike"
)

// runHook must never write to stdout or exit non-zero: Copilot denies the
// spawn if a command preToolUse hook errors, and any stdout JSON could be
// read as a decision. Every failure goes to errors.log instead.
func runHook(args []string) {
	var dir string
	defer func() {
		if r := recover(); r != nil {
			logHookError(dir, fmt.Errorf("panic: %v", r))
		}
		os.Exit(0)
	}()
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "which hook config fired, e.g. claude-style or copilot-native")
	event := fs.String("event", "", "event name, for payloads that don't carry one")
	logAll := fs.Bool("log-all", false, "log every tool call, not just subagent spawns")
	fs.StringVar(&dir, "dir", "", "log directory (default $MODEL_ROUTER_SPIKE_DIR or ~/.model-router-spike)")
	parseErr := fs.Parse(args)
	if parseErr != nil || fs.NArg() > 0 {
		if d := scanDir(args); d != "" {
			dir = d
		}
	}
	if dir == "" {
		d, err := spike.DefaultDir()
		if err != nil {
			return // nowhere to log; stay silent
		}
		dir = d
	}
	if parseErr != nil {
		logHookError(dir, fmt.Errorf("flags: %w", parseErr))
	} else if fs.NArg() > 0 {
		logHookError(dir, fmt.Errorf("flags: unexpected arguments %q", fs.Args()))
	}
	err := spike.RunHook(os.Stdin, spike.HookOptions{Source: *source, Event: *event, LogAll: *logAll, Dir: dir})
	if err != nil {
		logHookError(dir, err)
	}
}

func logHookError(dir string, err error) {
	if dir == "" {
		return
	}
	line := fmt.Sprintf("%s %v", time.Now().UTC().Format(time.RFC3339), err)
	_ = spike.AppendLine(filepath.Join(dir, spike.ErrorsFile), []byte(line))
}
