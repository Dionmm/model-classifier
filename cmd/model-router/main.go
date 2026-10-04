package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Dionmm/model-classifier/internal/client"
	"github.com/Dionmm/model-classifier/internal/payload"
	"github.com/Dionmm/model-classifier/internal/wire"
)

func main() {
	started := time.Now()
	code := 0
	defer func() {
		if recover() != nil {
			code = 0
		}
		os.Exit(code)
	}()
	code = run(started)
}

func run(started time.Time) int {
	if len(os.Args) < 2 {
		return 1
	}
	switch os.Args[1] {
	case "hook":
		hook(started, os.Args[2:])
		return 0
	case "doctor":
		return doctor(os.Args[2:])
	case "verify":
		return verify(os.Args[2:])
	default:
		return 1
	}
}

func hook(started time.Time, args []string) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})
	var harness, socket string
	fs.StringVar(&harness, "harness", "", "claude or copilot")
	fs.StringVar(&socket, "socket", "", "unix socket path")
	if fs.Parse(args) != nil {
		return
	}
	client.Hook(client.Options{Harness: harness, SocketPath: socket, StartedAt: started})
}

func doctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	var bin, socket, config string
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	fs.StringVar(&bin, "bin", exe, "model-router binary path")
	fs.StringVar(&socket, "socket", wire.SocketPath(), "unix socket path")
	fs.StringVar(&config, "config", wire.ConfigPath(), "config path")
	if fs.Parse(args) != nil {
		return 1
	}
	ok := true
	abs, err := filepath.Abs(bin)
	if err != nil || !filepath.IsAbs(abs) {
		fmt.Printf("binary: not absolute (%s)\n", bin)
		ok = false
	} else if st, err := os.Stat(abs); err != nil || st.Mode().Perm()&0111 == 0 {
		fmt.Printf("binary: not executable (%s)\n", abs)
		ok = false
	} else {
		fmt.Printf("binary: %s\n", abs)
	}
	if st, err := os.Stat(socket); err != nil || st.Mode()&os.ModeSocket == 0 {
		fmt.Printf("socket: unavailable (%s)\n", socket)
		ok = false
	} else {
		fmt.Printf("socket: %s\n", socket)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h, err := client.Health(ctx, socket)
	if err != nil {
		fmt.Printf("daemon health: %v\n", err)
		ok = false
	} else {
		fmt.Printf("daemon health: protocol=%d version=%s api_key=%t harnesses=%v breaker=%s\n", h.Protocol, h.Version, h.HasAPIKey, h.Harnesses, h.BreakerState)
		if !h.HasAPIKey {
			ok = false
		}
	}
	if _, err := os.ReadFile(config); err != nil {
		fmt.Printf("config: unreadable (%s): %v\n", config, err)
		ok = false
	} else {
		fmt.Printf("config: %s\n", config)
	}
	fmt.Printf("\nClaude hook snippet:\n")
	fmt.Printf("{\n  \"hooks\": {\n    \"PreToolUse\": [{\"matcher\": \"Agent|Task\", \"hooks\": [{\"type\": \"command\", \"command\": %q, \"timeout\": 3}]}]\n  }\n}\n\n", abs+" hook --harness "+payload.ShapeClaude)
	fmt.Printf("Copilot hook snippet:\n")
	fmt.Printf("{\n  \"version\": 1,\n  \"hooks\": {\n    \"preToolUse\": [{\"type\": \"command\", \"matcher\": \"task\", \"bash\": %q, \"timeoutSec\": 3}]\n  }\n}\n", abs+" hook --harness "+payload.ShapeCopilot)
	if !ok {
		return 1
	}
	return 0
}

func verify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	var harness, action, socket string
	fs.StringVar(&harness, "harness", "", "claude or copilot")
	fs.StringVar(&action, "action", "", "force:<tier> or none")
	fs.StringVar(&socket, "socket", wire.SocketPath(), "unix socket path")
	if fs.Parse(args) != nil || harness == "" || action == "" {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := client.Verify(ctx, socket, harness, action)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return 1
	}
	fmt.Printf("nonce: %s\nexpires: %s\n\nPut the nonce in the spawn prompt. For Claude, correlate via PostToolUse tool_response.resolvedModel.\n", resp.Nonce, resp.ExpiresAt.Format(time.RFC3339))
	return 0
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
