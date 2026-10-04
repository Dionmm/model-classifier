package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Dionmm/model-classifier/internal/daemon"
	"github.com/Dionmm/model-classifier/internal/payload"
	"github.com/Dionmm/model-classifier/internal/router"
	"github.com/Dionmm/model-classifier/internal/wire"
)

func TestWaitForShutdownBlocksUntilDrainDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		waitForShutdown(ctx, done)
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("waitForShutdown returned before drain/log/telemetry shutdown completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(done)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("waitForShutdown did not return after shutdown completed")
	}
}

func TestSubprocessSIGTERMDrainsInFlightRequestAndCleansUp(t *testing.T) {
	bin := buildModelRouterd(t)
	jevStarted := make(chan struct{}, 1)
	fakeJev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jevStarted <- struct{}{}
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"tier":{"type":"choice","choice":"deep","confidence":0.95,"probabilities":{"fast":0.01,"balanced":0.02,"deep":0.95,"max":0.02}}},"usage":{"input_tokens":12}}`))
	}))
	defer fakeJev.Close()

	home, err := os.MkdirTemp("/tmp", "mrhome")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	keyPath := filepath.Join(home, "key")
	if err := os.WriteFile(keyPath, []byte("test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := daemon.DefaultConfig()
	cfg.JevEndpoint = fakeJev.URL
	cfg.APIKeyFile = keyPath
	h := cfg.Harnesses[payload.ShapeClaude]
	h.Enabled = true
	cfg.Harnesses[payload.ShapeClaude] = h
	cfgPath := filepath.Join(home, "config.json")
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, cfgBytes, 0600); err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp("/tmp", "mrs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "r.sock")

	cmd := exec.Command(bin, "-config", cfgPath, "-socket", sock)
	cmd.Env = append(os.Environ(), "HOME="+home, "OTEL_SDK_DISABLED=true")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	waitForSocket(t, sock)
	body := claudePayload("haiku", "slow drain prompt")
	done := make(chan []byte, 1)
	errs := make(chan error, 1)
	go func() {
		out, err := postUnix(sock, payload.ShapeClaude, body)
		if err != nil {
			errs <- err
			return
		}
		done <- out
	}()
	select {
	case <-jevStarted:
	case err := <-errs:
		t.Fatalf("route request failed before Jev started: %v; stderr=%s", err, stderr.String())
	case <-time.After(2 * time.Second):
		t.Fatalf("fake Jev did not receive in-flight request; stderr=%s", stderr.String())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var out []byte
	select {
	case out = <-done:
	case err := <-errs:
		t.Fatalf("in-flight request did not complete during SIGTERM drain: %v; stderr=%s", err, stderr.String())
	case <-time.After(3 * time.Second):
		t.Fatalf("in-flight request timed out during SIGTERM drain; stderr=%s", stderr.String())
	}
	sp, ok := payload.ExtractSpawn(body)
	if !ok {
		t.Fatal("test payload not spawn")
	}
	want, err := router.BuildOutput(payload.ShapeClaude, sp.Args, "opus")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("in-flight response = %s, want exact %s", out, want)
	}
	if err := cmd.Wait(); err != nil {
		if ee := new(exec.ExitError); errors.As(err, &ee) {
			t.Fatalf("model-routerd exit code = %d stderr=%s", ee.ExitCode(), stderr.String())
		}
		t.Fatalf("model-routerd wait: %v stderr=%s", err, stderr.String())
	}
	decisionPath := filepath.Join(home, ".model-router", "decisions.jsonl")
	decision, err := os.ReadFile(decisionPath)
	if err != nil {
		t.Fatalf("decision log not written after shutdown drain: %v", err)
	}
	if !bytes.Contains(decision, []byte(`"tool_use_id":"tu"`)) || !bytes.Contains(decision, []byte(`"reason":"override"`)) {
		t.Fatalf("decision log missing drained request line: %s", decision)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket cleanup after shutdown: stat err=%v, want not exist", err)
	}
}

func buildModelRouterd(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "model-routerd")
	cmd := exec.Command("go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("go env GOMOD = %q, want module path", gomod)
	}
	cmd = exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/model-routerd")
	cmd.Dir = filepath.Dir(gomod)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build model-routerd: %v\n%s", err, out)
	}
	return bin
}

func waitForSocket(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("unix", sock, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s did not become available", sock)
}

func postUnix(sock, harness string, body []byte) ([]byte, error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	_ = conn.SetDeadline(deadline)
	var req bytes.Buffer
	req.WriteString("POST " + wire.PathRoute + " HTTP/1.1\r\n")
	req.WriteString("Host: unix\r\nConnection: close\r\n")
	req.WriteString(wire.HeaderProtocol + ": " + wire.ProtocolVersion + "\r\n")
	req.WriteString(wire.HeaderHarness + ": " + harness + "\r\n")
	req.WriteString(wire.HeaderDeadline + ": " + strconv.FormatInt(deadline.UnixMilli(), 10) + "\r\n")
	req.WriteString("Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n")
	req.Write(body)
	if _, err := conn.Write(req.Bytes()); err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func claudePayload(model, prompt string) []byte {
	args := `{"subagent_type":"research","description":"desc","prompt":` + quote(prompt)
	if model != "" {
		args += `,"model":` + quote(model)
	}
	args += `}`
	return []byte(`{"session_id":"s","tool_name":"Agent","tool_use_id":"tu","tool_input":` + args + `}`)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
