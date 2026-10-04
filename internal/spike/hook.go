package spike

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// MaxPayloadBytes caps how much stdin the hook will read.
const MaxPayloadBytes = 64 << 20

// RecordVersion is bumped when the log line schema changes.
const RecordVersion = 1

// Record is one JSONL line in payloads.jsonl.
type Record struct {
	V          int             `json:"v"`
	TS         time.Time       `json:"ts"`
	Source     string          `json:"source"`
	Event      string          `json:"event"`
	Shape      string          `json:"shape"`
	ToolName   string          `json:"tool_name"`
	SessionID  string          `json:"session_id,omitempty"`
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	StdinBytes int             `json:"stdin_bytes"`
	Truncated  bool            `json:"truncated,omitempty"`
	ParseMicro int64           `json:"parse_us"`
	EnvNames   []string        `json:"env_names"`
	Derived    *Derived        `json:"derived,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
	RawText    string          `json:"raw_text,omitempty"`
}

// HookOptions configure one hook invocation.
type HookOptions struct {
	Source  string // which hook config fired, e.g. "claude-style" or "copilot-native"
	Event   string // event name when the payload doesn't carry one
	LogAll  bool   // log non-spawn tool calls too
	Dir     string
	Now     func() time.Time
	Environ func() []string
}

// RunHook reads one payload from r and, if it is a subagent spawn, appends a
// record to the log. It writes nothing to stdout, so the harness proceeds as
// if no hook had run. Returned errors are for the caller to log; they must
// never change the exit code.
func RunHook(r io.Reader, o HookOptions) error {
	start := time.Now()
	raw, err := io.ReadAll(io.LimitReader(r, MaxPayloadBytes+1))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	truncated := len(raw) > MaxPayloadBytes
	if truncated {
		raw = raw[:MaxPayloadBytes]
	}
	p := Parse(raw)
	if !p.IsSpawn && !o.LogAll {
		return nil
	}
	now, environ := time.Now, os.Environ
	if o.Now != nil {
		now = o.Now
	}
	if o.Environ != nil {
		environ = o.Environ
	}
	event := p.Event
	if event == "" {
		event = o.Event
	}
	rec := Record{
		V:          RecordVersion,
		TS:         now().UTC(),
		Source:     o.Source,
		Event:      event,
		Shape:      p.Shape,
		ToolName:   p.ToolName,
		SessionID:  p.SessionID,
		ToolUseID:  p.ToolUseID,
		StdinBytes: len(raw),
		Truncated:  truncated,
		EnvNames:   envNames(environ()),
		Derived:    p.Derived,
	}
	if !truncated && json.Valid(raw) {
		rec.Raw = raw
	} else {
		rec.RawText = string(raw)
	}
	rec.ParseMicro = time.Since(start).Microseconds()
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	return AppendLine(filepath.Join(o.Dir, PayloadsFile), line)
}
