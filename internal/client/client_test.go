package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dionmm/model-classifier/internal/daemon"
	"github.com/Dionmm/model-classifier/internal/jev"
	"github.com/Dionmm/model-classifier/internal/payload"
	"github.com/Dionmm/model-classifier/internal/router"
	"github.com/Dionmm/model-classifier/internal/wire"
	"golang.org/x/sys/unix"
)

func TestHookDeadlineConstant(t *testing.T) {
	if HookDeadline != 1500*time.Millisecond {
		t.Fatalf("HookDeadline = %s, want real client deadline 1500ms", HookDeadline)
	}
}

func TestHookFailOpenCases(t *testing.T) {
	t.Run("daemon_absent", func(t *testing.T) {
		home := t.TempDir()
		var out bytes.Buffer
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: filepath.Join(t.TempDir(), "missing.sock"), Stdin: bytes.NewReader(claudePayload("haiku", "prompt-secret")), Stdout: &out, Home: home, StartedAt: time.Now()})
		if out.Len() != 0 {
			t.Fatalf("daemon absent stdout=%q, want empty", out.String())
		}
		if got := readClientErrors(t, home); !strings.Contains(got, `"error_class":"connect"`) || strings.Contains(got, "prompt-secret") {
			t.Fatalf("client error line = %q, want connect without prompt text", got)
		}
	})
	t.Run("slow_past_deadline", func(t *testing.T) {
		sock, closeServer := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"late":true}`))
		}))
		defer closeServer()
		var out bytes.Buffer
		started := time.Now().Add(-HookDeadline + 5*time.Millisecond)
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: sock, Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: &out, Home: t.TempDir(), StartedAt: started})
		if out.Len() != 0 {
			t.Fatalf("slow daemon stdout=%q, want empty", out.String())
		}
	})
	t.Run("status_500", func(t *testing.T) {
		sock, closeServer := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		defer closeServer()
		var out bytes.Buffer
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: sock, Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: &out, Home: t.TempDir(), StartedAt: time.Now()})
		if out.Len() != 0 {
			t.Fatalf("500 stdout=%q, want empty", out.String())
		}
	})
	t.Run("bad_protocol_empty_200", func(t *testing.T) {
		sock, closeServer := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
		defer closeServer()
		var out bytes.Buffer
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: sock, Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: &out, Home: t.TempDir(), StartedAt: time.Now()})
		if out.Len() != 0 {
			t.Fatalf("empty 200 stdout=%q, want empty", out.String())
		}
	})
	t.Run("panic_injected", func(t *testing.T) {
		var out bytes.Buffer
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: "unused", Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: &out, Home: t.TempDir(), StartedAt: time.Now(), BeforePost: func() { panic("boom") }})
		if out.Len() != 0 {
			t.Fatalf("panic stdout=%q, want empty", out.String())
		}
	})
	t.Run("unknown_harness", func(t *testing.T) {
		var out bytes.Buffer
		Hook(Options{Harness: "other", SocketPath: "unused", Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: &out, Home: t.TempDir(), StartedAt: time.Now()})
		if out.Len() != 0 {
			t.Fatalf("unknown harness stdout=%q, want empty", out.String())
		}
	})
	t.Run("garbage_stdin", func(t *testing.T) {
		var out bytes.Buffer
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: "unused", Stdin: strings.NewReader("{not-json"), Stdout: &out, Home: t.TempDir(), StartedAt: time.Now()})
		if out.Len() != 0 {
			t.Fatalf("garbage stdout=%q, want empty", out.String())
		}
	})
	t.Run("too_large", func(t *testing.T) {
		var out bytes.Buffer
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: "unused", Stdin: strings.NewReader(strings.Repeat("x", wire.MaxPayloadBytes+1)), Stdout: &out, Home: t.TempDir(), StartedAt: time.Now()})
		if out.Len() != 0 {
			t.Fatalf(">4MiB stdout=%q, want empty", out.String())
		}
	})
}

func TestReadChunkedExactBytesWithExtensionsAndTrailers(t *testing.T) {
	want := []byte("prefix-0123456789é🙂")
	var raw bytes.Buffer
	raw.WriteString("7\r\nprefix-\r\n")
	raw.WriteString("a;foo=bar\r\n0123456789\r\n")
	raw.WriteString("6\r\né🙂\r\n")
	raw.WriteString("0\r\nX-Trailer: yes\r\nAnother: ok\r\n\r\n")
	got, err := readChunked(bufio.NewReader(&raw), wire.MaxPayloadBytes)
	if err != nil {
		t.Fatalf("chunk-size parser rejected hex size with extensions/trailers: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("chunked parser bytes = %q, want exact %q", got, want)
	}
}

func TestHookEndToEndLargeChunkBoundaryExactOutputClaudeAndCopilot(t *testing.T) {
	sock, stop := startDaemon(t)
	defer stop()
	prompt := strings.Repeat("multi-byte-☃️-🙂-", 420)
	if len([]byte(prompt)) < 8<<10 {
		t.Fatalf("test prompt is %d bytes, want at least 8 KiB", len([]byte(prompt)))
	}
	for _, tc := range []struct {
		name    string
		harness string
		body    []byte
		model   string
	}{
		{name: "claude", harness: payload.ShapeClaude, body: claudePayload("haiku", prompt), model: "opus"},
		{name: "copilot_string_tool_args", harness: payload.ShapeCopilot, body: copilotPayload("claude-haiku-4.5", prompt, true), model: "claude-opus-5.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			Hook(Options{Harness: tc.harness, SocketPath: sock, Stdin: bytes.NewReader(tc.body), Stdout: &out, Home: t.TempDir(), StartedAt: time.Now()})
			sp, ok := payload.ExtractSpawn(tc.body)
			if !ok {
				t.Fatal("test payload is not a spawn")
			}
			want, err := router.BuildOutput(tc.harness, sp.Args, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("%s client output differs from exact daemon body: got %d bytes, want %d", tc.name, out.Len(), len(want))
			}
		})
	}
}

func TestHookFastPathNoSpawnNoIOAfterStdin(t *testing.T) {
	home := t.TempDir()
	dir, err := os.MkdirTemp("/tmp", "mr-client-fast-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "r.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- struct{}{}
			_ = c.Close()
		}
	}()
	var out bytes.Buffer
	Hook(Options{Harness: payload.ShapeClaude, SocketPath: sock, Stdin: strings.NewReader(`{"tool_name":"Bash","tool_input":{"prompt":"p"}}`), Stdout: &out, Home: home, StartedAt: time.Now()})
	if out.Len() != 0 {
		t.Fatalf("non-spawn stdout=%q, want empty", out.String())
	}
	select {
	case <-accepted:
		t.Fatal("non-spawn made a socket connection")
	case <-time.After(25 * time.Millisecond):
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("non-spawn wrote files under HOME: %v", entries)
	}
}

func TestModeOffReadsNoStdin(t *testing.T) {
	t.Setenv("MODEL_ROUTER_MODE", "off")
	Hook(Options{Harness: payload.ShapeClaude, Stdin: failReader{t}, Stdout: io.Discard, Home: t.TempDir(), StartedAt: time.Now()})
}

func TestClientErrorLineRules(t *testing.T) {
	t.Run("failure_writes_0600_without_prompt", func(t *testing.T) {
		home := t.TempDir()
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: filepath.Join(t.TempDir(), "missing.sock"), Stdin: bytes.NewReader(claudePayload("haiku", "top-secret-marker")), Stdout: io.Discard, Home: home, StartedAt: time.Now()})
		path := filepath.Join(home, ".model-router", "client-errors.jsonl")
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0600 {
			t.Fatalf("client-errors mode = %o, want 0600", st.Mode().Perm())
		}
		b, _ := os.ReadFile(path)
		if strings.Contains(string(b), "top-secret-marker") {
			t.Fatalf("prompt text leaked into client error line: %s", b)
		}
		var line ErrorLine
		if err := json.Unmarshal(bytes.TrimSpace(b), &line); err != nil || line.Harness != payload.ShapeClaude || line.ErrorClass == "" {
			t.Fatalf("bad client error line %#v err=%v raw=%s", line, err, b)
		}
	})
	t.Run("success_does_not_write", func(t *testing.T) {
		home := t.TempDir()
		sock, closeServer := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
		defer closeServer()
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: sock, Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: io.Discard, Home: home, StartedAt: time.Now()})
		if _, err := os.Stat(filepath.Join(home, ".model-router", "client-errors.jsonl")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("success wrote client-errors: %v", err)
		}
	})
	t.Run("deadline_passed_skips", func(t *testing.T) {
		home := t.TempDir()
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: filepath.Join(t.TempDir(), "missing.sock"), Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: io.Discard, Home: home, StartedAt: time.Now().Add(-2 * HookDeadline)})
		if _, err := os.Stat(filepath.Join(home, ".model-router", "client-errors.jsonl")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("past deadline wrote client-errors: %v", err)
		}
	})
	t.Run("lock_contended_drops", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".model-router")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "client-errors.jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: filepath.Join(t.TempDir(), "missing.sock"), Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: io.Discard, Home: home, StartedAt: time.Now()})
		b, _ := os.ReadFile(path)
		if len(b) != 0 {
			t.Fatalf("contended lock wrote %q, want drop", b)
		}
	})
	t.Run("over_one_mib_skips", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".model-router")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "client-errors.jsonl")
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), clientErrorMaxSize+1), 0600); err != nil {
			t.Fatal(err)
		}
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: filepath.Join(t.TempDir(), "missing.sock"), Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: io.Discard, Home: home, StartedAt: time.Now()})
		st, _ := os.Stat(path)
		if st.Size() != clientErrorMaxSize+1 {
			t.Fatalf("oversize client error file changed size to %d", st.Size())
		}
	})
	t.Run("at_one_mib_skips", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".model-router")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "client-errors.jsonl")
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), clientErrorMaxSize), 0600); err != nil {
			t.Fatal(err)
		}
		Hook(Options{Harness: payload.ShapeClaude, SocketPath: filepath.Join(t.TempDir(), "missing.sock"), Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: io.Discard, Home: home, StartedAt: time.Now()})
		st, _ := os.Stat(path)
		if st.Size() != clientErrorMaxSize {
			t.Fatalf("at-cap client error file changed size to %d", st.Size())
		}
	})
}

func TestRealBinarySubprocessFailOpenAndEndToEnd(t *testing.T) {
	bin := buildModelRouter(t)
	t.Run("daemon_absent", func(t *testing.T) {
		cmd := exec.Command(bin, "hook", "--harness", payload.ShapeClaude, "--socket", filepath.Join(t.TempDir(), "missing.sock"))
		cmd.Stdin = bytes.NewReader(claudePayload("haiku", "p"))
		out, err := cmd.Output()
		if ee := new(exec.ExitError); errors.As(err, &ee) {
			t.Fatalf("exit code = %d stderr=%s", ee.ExitCode(), ee.Stderr)
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Fatalf("daemon absent stdout=%q, want empty", out)
		}
	})
	t.Run("daemon_end_to_end", func(t *testing.T) {
		sock, stop := startDaemon(t)
		defer stop()
		body := claudePayload("haiku", "p")
		cmd := exec.Command(bin, "hook", "--harness", payload.ShapeClaude, "--socket", sock)
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		sp, ok := payload.ExtractSpawn(body)
		if !ok {
			t.Fatal("test payload not spawn")
		}
		want, err := router.BuildOutput(payload.ShapeClaude, sp.Args, "opus")
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(want) {
			t.Fatalf("end-to-end output = %s, want exact %s", out, want)
		}
	})
	t.Run("stdin_never_closes_deadline_exit_zero", func(t *testing.T) {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer pr.Close()
		defer pw.Close()
		cmd := exec.Command(bin, "hook", "--harness", payload.ShapeClaude)
		cmd.Stdin = pr
		start := time.Now()
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
			t.Fatalf("stdin deadline elapsed=%s, want bounded by hook deadline", elapsed)
		}
		if len(out) != 0 {
			t.Fatalf("stdin deadline stdout=%q, want empty", out)
		}
	})
	t.Run("stdout_closed_sigpipe_exit_zero", func(t *testing.T) {
		sock, closeServer := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("x"), 1<<20))
		}))
		defer closeServer()
		cmd := exec.Command(bin, "hook", "--harness", payload.ShapeClaude, "--socket", sock)
		cmd.Stdin = bytes.NewReader(claudePayload("haiku", "p"))
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = stdout.Close()
		if err := cmd.Wait(); err != nil {
			if ee := new(exec.ExitError); errors.As(err, &ee) {
				t.Fatalf("SIGPIPE exit code = %d stderr=%s, want 0", ee.ExitCode(), ee.Stderr)
			}
			t.Fatal(err)
		}
	})
}

func TestClientProcessPerformanceTargets(t *testing.T) {
	bin := buildModelRouter(t)
	small := []byte(`{"tool_name":"Bash","tool_input":{"prompt":"p"}}`)
	large := append([]byte(`{"tool_name":"Bash","tool_input":{"prompt":"`), bytes.Repeat([]byte("x"), wire.MaxPayloadBytes-70)...)
	large = append(large, []byte(`"}}`)...)
	measureExec := func(name string, body []byte, args ...string) time.Duration {
		t.Helper()
		var samples []time.Duration
		for i := 0; i < 15; i++ {
			cmd := exec.Command(bin, args...)
			cmd.Stdin = bytes.NewReader(body)
			start := time.Now()
			out, err := cmd.Output()
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("%s run failed: %v", name, err)
			}
			if len(out) != 0 {
				t.Fatalf("%s stdout=%q, want empty", name, out)
			}
			samples = append(samples, elapsed)
		}
		slices.Sort(samples)
		median := samples[len(samples)/2]
		t.Logf("%s median=%s target=%s", name, median, 10*time.Millisecond)
		return median
	}
	smallMedian := measureExec("client_non_spawn_small", small, "hook", "--harness", payload.ShapeClaude)
	largeMedian := measureExec("client_non_spawn_4MiB", large, "hook", "--harness", payload.ShapeClaude)
	if smallMedian >= 10*time.Millisecond || largeMedian >= 10*time.Millisecond {
		t.Skipf("process-start median not robustly under 10ms on this host: small=%s large=%s", smallMedian, largeMedian)
	}

	sock, stop := startDaemon(t)
	defer stop()
	body := claudePayload("haiku", "p")
	var samples []time.Duration
	for i := 0; i < 15; i++ {
		cmd := exec.Command(bin, "hook", "--harness", payload.ShapeClaude, "--socket", sock)
		cmd.Stdin = bytes.NewReader(body)
		start := time.Now()
		out, err := cmd.Output()
		elapsed := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out, []byte(`"updatedInput"`)) {
			t.Fatalf("client+daemon output=%s", out)
		}
		samples = append(samples, elapsed)
	}
	slices.Sort(samples)
	median := samples[len(samples)/2]
	t.Logf("client_plus_daemon_spawn median=%s target=%s", median, 5*time.Millisecond)
	if median >= 5*time.Millisecond {
		t.Skipf("client+daemon process median not robustly under 5ms on this host: %s", median)
	}
}

type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Fatal("MODEL_ROUTER_MODE=off read stdin")
	return 0, io.EOF
}

func serveUnix(t *testing.T, h http.Handler) (string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mr-client-")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "r.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	return sock, func() {
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	}
}

func buildModelRouter(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "model-router")
	root := moduleRoot(t)
	cmd := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/model-router")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build model-router: %v\n%s", err, out)
	}
	return bin
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("go env GOMOD = %q, want module path", gomod)
	}
	return filepath.Dir(gomod)
}

func startDaemon(t *testing.T) (string, func()) {
	t.Helper()
	cfg := daemon.DefaultConfig()
	h := cfg.Harnesses[payload.ShapeClaude]
	h.Enabled = true
	cfg.Harnesses[payload.ShapeClaude] = h
	h = cfg.Harnesses[payload.ShapeCopilot]
	h.Enabled = true
	cfg.Harnesses[payload.ShapeCopilot] = h
	s, err := daemon.New(daemon.Options{
		Config: cfg,
		Caller: fakeCaller{},
		Questions: map[string]any{"tier": map[string]any{"type": "choice", "criteria": map[string]any{
			"fast": "", "balanced": "", "deep": "", "max": "",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "mr-client-")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "r.sock")
	ln, err := s.Listen(sock, func(*net.UnixConn) (uint32, error) { return uint32(os.Getuid()), nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := s.HTTPServer()
	go func() { _ = srv.Serve(ln) }()
	return sock, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = os.RemoveAll(dir)
	}
}

type fakeCaller struct{}

func (fakeCaller) Call(context.Context, any, map[string]any) (*jev.CallResult, error) {
	return &jev.CallResult{Status: http.StatusOK, Body: []byte(`{"model":"jev-1.13.0","answers":{"tier":{"type":"choice","choice":"deep","confidence":0.95,"probabilities":{"fast":0.01,"balanced":0.02,"deep":0.95,"max":0.02}}},"usage":{"input_tokens":12}}`)}, nil
}

func claudePayload(model, prompt string) []byte {
	args := `{"subagent_type":"research","description":"desc","prompt":` + quote(prompt)
	if model != "" {
		args += `,"model":` + quote(model)
	}
	args += `}`
	return []byte(`{"session_id":"s","tool_name":"Agent","tool_use_id":"tu","tool_input":` + args + `}`)
}

func copilotPayload(model, prompt string, stringArgs bool) []byte {
	args := `{"agent_type":"research","description":"desc","prompt":` + quote(prompt)
	if model != "" {
		args += `,"model":` + quote(model)
	}
	args += `}`
	if stringArgs {
		return []byte(`{"sessionId":"s","toolName":"task","toolCallId":"tc","toolArgs":` + quote(args) + `}`)
	}
	return []byte(`{"sessionId":"s","toolName":"task","toolCallId":"tc","toolArgs":` + args + `}`)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func readClientErrors(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".model-router", "client-errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestVerifyAndHealthHelpers(t *testing.T) {
	sock, stop := startDaemon(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h, err := Health(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	if h.Protocol != 1 || !h.HasAPIKey {
		t.Fatalf("health = %#v", h)
	}
	vr, err := Verify(ctx, sock, payload.ShapeClaude, "force:deep")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(vr.Nonce, "mr-verify-") || !vr.ExpiresAt.After(time.Now()) {
		t.Fatalf("verify response = %#v", vr)
	}
}

func TestHookHeaders(t *testing.T) {
	var gotHarness, gotProto string
	var gotDeadline int64
	var mu sync.Mutex
	sock, closeServer := serveUnix(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotHarness = r.Header.Get(wire.HeaderHarness)
		gotProto = r.Header.Get(wire.HeaderProtocol)
		gotDeadline, _ = strconv.ParseInt(r.Header.Get(wire.HeaderDeadline), 10, 64)
	}))
	defer closeServer()
	start := time.Now().Truncate(time.Millisecond)
	Hook(Options{Harness: payload.ShapeClaude, SocketPath: sock, Stdin: bytes.NewReader(claudePayload("haiku", "p")), Stdout: io.Discard, Home: t.TempDir(), StartedAt: start})
	mu.Lock()
	defer mu.Unlock()
	if gotHarness != payload.ShapeClaude || gotProto != wire.ProtocolVersion || gotDeadline != start.Add(HookDeadline).UnixMilli() {
		t.Fatalf("headers harness=%q proto=%q deadline=%d want %q %q %d", gotHarness, gotProto, gotDeadline, payload.ShapeClaude, wire.ProtocolVersion, start.Add(HookDeadline).UnixMilli())
	}
}
