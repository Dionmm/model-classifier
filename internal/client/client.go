package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Dionmm/model-classifier/internal/payload"
	"github.com/Dionmm/model-classifier/internal/wire"
)

const (
	HookDeadline       = 1500 * time.Millisecond
	clientErrorMaxSize = 1 << 20
)

type Options struct {
	Harness    string
	SocketPath string
	Stdin      io.Reader
	Stdout     io.Writer
	Home       string
	StartedAt  time.Time
	Now        func() time.Time
	BeforePost func()
}

type ErrorLine struct {
	TS         time.Time `json:"ts"`
	Harness    string    `json:"harness"`
	ErrorClass string    `json:"error_class"`
}

func Hook(o Options) {
	signal.Ignore(syscall.SIGPIPE)
	defer func() {
		_ = recover()
	}()
	if os.Getenv("MODEL_ROUTER_MODE") == "off" {
		return
	}
	if o.Harness != payload.ShapeClaude && o.Harness != payload.ShapeCopilot {
		return
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.StartedAt.IsZero() {
		o.StartedAt = time.Now()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.SocketPath == "" {
		o.SocketPath = socketPathFromEnv()
	}
	deadline := o.StartedAt.Add(HookDeadline)
	raw, ok := readPayload(o.Stdin, deadline)
	if !ok {
		return
	}
	tool, ok := topLevelToolName(raw)
	if !ok || !payload.IsSpawnTool(tool) {
		return
	}
	if o.BeforePost != nil {
		o.BeforePost()
	}
	body, failure := postRoute(o, deadline, raw)
	if failure != "" && o.Now().Before(deadline) {
		appendClientError(o, failure)
		return
	}
	if len(body) == 0 {
		return
	}
	_, _ = o.Stdout.Write(body)
}

func Verify(ctx context.Context, socketPath, harness, action string) (wire.VerifyResponse, error) {
	if harness != payload.ShapeClaude && harness != payload.ShapeCopilot {
		return wire.VerifyResponse{}, fmt.Errorf("unknown harness %q", harness)
	}
	b, err := json.Marshal(wire.VerifyRequest{Harness: harness, Action: action})
	if err != nil {
		return wire.VerifyResponse{}, err
	}
	resp, err := doSocket(ctx, socketPathOrDefault(socketPath), "POST", wire.PathVerify, nil, b)
	if err != nil {
		return wire.VerifyResponse{}, err
	}
	if resp.status != 200 {
		return wire.VerifyResponse{}, fmt.Errorf("verify status %d", resp.status)
	}
	var vr wire.VerifyResponse
	if err := json.NewDecoder(io.LimitReader(bytes.NewReader(resp.body), 4096)).Decode(&vr); err != nil {
		return wire.VerifyResponse{}, err
	}
	return vr, nil
}

func Health(ctx context.Context, socketPath string) (wire.HealthResponse, error) {
	resp, err := doSocket(ctx, socketPathOrDefault(socketPath), "GET", wire.PathHealth, nil, nil)
	if err != nil {
		return wire.HealthResponse{}, err
	}
	if resp.status != 200 {
		return wire.HealthResponse{}, fmt.Errorf("health status %d", resp.status)
	}
	var h wire.HealthResponse
	if err := json.NewDecoder(io.LimitReader(bytes.NewReader(resp.body), 4096)).Decode(&h); err != nil {
		return wire.HealthResponse{}, err
	}
	if strconv.Itoa(h.Protocol) != wire.ProtocolVersion {
		return wire.HealthResponse{}, fmt.Errorf("protocol %d, want %s", h.Protocol, wire.ProtocolVersion)
	}
	return h, nil
}

func postRoute(o Options, deadline time.Time, raw []byte) ([]byte, string) {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	headers := map[string]string{
		wire.HeaderProtocol: wire.ProtocolVersion,
		wire.HeaderHarness:  o.Harness,
		wire.HeaderDeadline: strconv.FormatInt(deadline.UnixMilli(), 10),
	}
	resp, err := doSocket(ctx, o.SocketPath, "POST", wire.PathRoute, headers, raw)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, "deadline"
		}
		return nil, "connect"
	}
	if resp.status != 200 {
		return nil, "status_" + strconv.Itoa(resp.status)
	}
	if len(resp.body) > wire.MaxPayloadBytes {
		return nil, "too_large"
	}
	return resp.body, ""
}

type socketResponse struct {
	status int
	body   []byte
}

func doSocket(ctx context.Context, socketPath, method, path string, headers map[string]string, body []byte) (socketResponse, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return socketResponse{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	var req bytes.Buffer
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: unix\r\nConnection: close\r\nContent-Length: %d\r\n", method, path, len(body))
	for k, v := range headers {
		fmt.Fprintf(&req, "%s: %s\r\n", k, v)
	}
	req.WriteString("\r\n")
	req.Write(body)
	if _, err := conn.Write(req.Bytes()); err != nil {
		return socketResponse{}, err
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return socketResponse{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 {
		return socketResponse{}, fmt.Errorf("bad status line %q", strings.TrimSpace(line))
	}
	status, err := strconv.Atoi(parts[1])
	if err != nil {
		return socketResponse{}, err
	}
	contentLength := -1
	chunked := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return socketResponse{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "content-length":
			contentLength, _ = strconv.Atoi(strings.TrimSpace(value))
		case "transfer-encoding":
			chunked = strings.Contains(strings.ToLower(value), "chunked")
		}
	}
	var rb []byte
	if chunked {
		rb, err = readChunked(br, wire.MaxPayloadBytes+1)
	} else if contentLength >= 0 {
		if contentLength > wire.MaxPayloadBytes+1 {
			return socketResponse{status: status, body: make([]byte, wire.MaxPayloadBytes+1)}, nil
		}
		rb = make([]byte, contentLength)
		_, err = io.ReadFull(br, rb)
	} else {
		rb, err = io.ReadAll(io.LimitReader(br, wire.MaxPayloadBytes+1))
	}
	if err != nil {
		return socketResponse{}, err
	}
	return socketResponse{status: status, body: rb}, nil
}

func readChunked(br *bufio.Reader, limit int) ([]byte, error) {
	var out bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeText, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		n, err := strconv.ParseInt(sizeText, 16, 64)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					return nil, err
				}
				if strings.TrimSpace(line) == "" {
					return out.Bytes(), nil
				}
			}
		}
		if out.Len()+int(n) > limit {
			return make([]byte, limit), nil
		}
		if _, err := io.CopyN(&out, br, n); err != nil {
			return nil, err
		}
		if _, err := br.ReadString('\n'); err != nil {
			return nil, err
		}
	}
}

func readPayload(r io.Reader, deadline time.Time) ([]byte, bool) {
	type result struct {
		b  []byte
		ok bool
	}
	done := make(chan result, 1)
	go func() {
		var b bytes.Buffer
		n, err := io.Copy(&b, io.LimitReader(r, int64(wire.MaxPayloadBytes)+1))
		if err != nil || n > int64(wire.MaxPayloadBytes) {
			done <- result{}
			return
		}
		done <- result{b: b.Bytes(), ok: true}
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case res := <-done:
		return res.b, res.ok
	case <-timer.C:
		return nil, false
	}
}

func topLevelToolName(raw []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return "", false
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		key, ok := tok.(string)
		if !ok {
			return "", false
		}
		if key == "tool_name" || key == "toolName" {
			tok, err = dec.Token()
			if err != nil {
				return "", false
			}
			s, ok := tok.(string)
			return s, ok
		}
		if err := skipValue(dec); err != nil {
			return "", false
		}
	}
	return "", false
}

func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			for dec.More() {
				if _, err := dec.Token(); err != nil {
					return err
				}
				if err := skipValue(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := skipValue(dec); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
	}
	return nil
}

func appendClientError(o Options, class string) {
	home := o.Home
	if home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		return
	}
	dir := filepath.Join(home, ".model-router")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	_ = os.Chmod(dir, 0700)
	path := filepath.Join(dir, "client-errors.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	if err := tryLock(f); err != nil {
		return
	}
	defer unlock(f)
	_ = f.Chmod(0600)
	line := ErrorLine{TS: o.Now().UTC(), Harness: o.Harness, ErrorClass: class}
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	b = append(b, '\n')
	st, err := f.Stat()
	if err != nil || st.Size() >= clientErrorMaxSize || st.Size()+int64(len(b)) > clientErrorMaxSize {
		return
	}
	_, _ = f.Write(b)
}

func socketPathFromEnv() string {
	if p := os.Getenv("MODEL_ROUTER_SOCKET"); p != "" {
		return p
	}
	return wire.SocketPath()
}

func socketPathOrDefault(p string) string {
	if p != "" {
		return p
	}
	return socketPathFromEnv()
}
