package telemetry

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const DefaultDecisionLogMaxBytes = int64(10 << 20)

type DecisionLine struct {
	TS                time.Time          `json:"ts"`
	Harness           string             `json:"harness,omitempty"`
	SessionID         string             `json:"session_id,omitempty"`
	ToolUseID         string             `json:"tool_use_id,omitempty"`
	OrchestratorModel string             `json:"orchestrator_model,omitempty"`
	OrchestratorTier  string             `json:"orchestrator_tier,omitempty"`
	JevTier           string             `json:"jev_tier,omitempty"`
	Confidence        float64            `json:"confidence,omitempty"`
	Probabilities     map[string]float64 `json:"probabilities,omitempty"`
	JevModel          string             `json:"jev_model_version,omitempty"`
	Decision          string             `json:"decision,omitempty"`
	Reason            string             `json:"reason"`
	StateBytes        int                `json:"state_bytes,omitempty"`
	InputTokens       int                `json:"input_tokens,omitempty"`
	JevLatencyMS      float64            `json:"jev_latency_ms,omitempty"`
	ErrorClass        string             `json:"error_class,omitempty"`
	Nonce             string             `json:"nonce,omitempty"`
}

type DecisionLog struct {
	path     string
	maxBytes int64
	keep     int
	ch       chan []byte
	done     chan struct{}
	metrics  Metrics
	dropped  atomic.Uint64
	once     sync.Once
	mu       sync.RWMutex
	closed   bool
	openFile func() (logFile, error)
}

type logFile interface {
	io.Writer
	Close() error
	Stat() (os.FileInfo, error)
}

func NewDecisionLog(path string, maxBytes int64, keep, buffer int, metrics Metrics) (*DecisionLog, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultDecisionLogMaxBytes
	}
	if keep <= 0 {
		keep = 3
	}
	if buffer <= 0 {
		buffer = 1024
	}
	d := &DecisionLog{path: path, maxBytes: maxBytes, keep: keep, ch: make(chan []byte, buffer), done: make(chan struct{}), metrics: metrics}
	go d.run()
	return d, nil
}

func (d *DecisionLog) Dropped() uint64 { return d.dropped.Load() }

func (d *DecisionLog) Record(line DecisionLine) {
	if line.TS.IsZero() {
		line.TS = time.Now().UTC()
	}
	b, err := json.Marshal(line)
	if err != nil {
		d.drop()
		return
	}
	b = append(b, '\n')
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		d.drop()
		return
	}
	select {
	case d.ch <- b:
	default:
		d.drop()
	}
}

func (d *DecisionLog) Close() error {
	d.once.Do(func() {
		d.mu.Lock()
		d.closed = true
		close(d.ch)
		d.mu.Unlock()
		<-d.done
	})
	return nil
}

func (d *DecisionLog) drop() { d.dropN(1) }

func (d *DecisionLog) dropN(n int) {
	for i := 0; i < n; i++ {
		d.dropped.Add(1)
		d.metrics.RecordLogDropped(context.Background())
	}
}

func (d *DecisionLog) run() {
	defer close(d.done)
	var f logFile
	var w *bufio.Writer
	// pending counts lines accepted into w but not yet flushed; a failed
	// flush loses all of them.
	pending := 0
	closeFile := func(flush bool) {
		if flush && w != nil {
			if err := w.Flush(); err != nil {
				d.dropN(pending)
			}
		}
		pending = 0
		if f != nil {
			_ = f.Close()
		}
		f, w = nil, nil
	}
	for b := range d.ch {
		if f == nil {
			var err error
			f, err = d.openLogFile()
			if err != nil {
				d.drop()
				continue
			}
			w = bufio.NewWriter(f)
		}
		if d.needsRotate(f, w, int64(len(b))) {
			closeFile(true)
			if err := d.rotate(); err != nil {
				d.drop()
				continue
			}
			var err error
			f, err = d.openLogFile()
			if err != nil {
				d.drop()
				continue
			}
			w = bufio.NewWriter(f)
		}
		// Flush first when b won't fit, so a failure inside Write can only
		// lose b itself. A line is written whole, directly, or copied whole
		// into the buffer, never split. Over-counts come only from a partial
		// underlying write during Flush: counted >= lost.
		if w.Buffered() > 0 && len(b) > w.Available() {
			if err := w.Flush(); err != nil {
				d.dropN(pending + 1)
				closeFile(false)
				continue
			}
			pending = 0
		}
		if _, err := w.Write(b); err != nil {
			d.drop()
			closeFile(false)
			continue
		}
		// A line bufio wrote directly (too big for an empty buffer) is already on disk.
		if w.Buffered() > 0 {
			pending++
		}
		if len(d.ch) == 0 {
			if err := w.Flush(); err != nil {
				d.dropN(pending)
				closeFile(false)
				continue
			}
			pending = 0
		}
	}
	closeFile(true)
}

func (d *DecisionLog) openLogFile() (logFile, error) {
	var f logFile
	var err error
	if d.openFile != nil {
		f, err = d.openFile()
	} else {
		f, err = d.open()
	}
	if err != nil {
		return nil, err
	}
	if err := repairTail(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// repairTail terminates a trailing partial line (left by a short write) so the
// next record starts on its own line; the fragment stays as one bad line.
func repairTail(f logFile) error {
	ra, ok := f.(io.ReaderAt)
	if !ok {
		return nil
	}
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return nil
	}
	var last [1]byte
	if _, err := ra.ReadAt(last[:], st.Size()-1); err != nil || last[0] == '\n' {
		return nil
	}
	_, err = f.Write([]byte{'\n'})
	return err
}

func (d *DecisionLog) open() (logFile, error) {
	if err := os.MkdirAll(filepath.Dir(d.path), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(d.path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(d.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (d *DecisionLog) needsRotate(f logFile, w *bufio.Writer, add int64) bool {
	st, err := f.Stat()
	buffered := int64(0)
	if w != nil {
		buffered = int64(w.Buffered())
	}
	return err == nil && st.Size()+buffered+add > d.maxBytes
}

func (d *DecisionLog) rotate() error {
	for i := d.keep; i >= 1; i-- {
		dst := fmt.Sprintf("%s.%d", d.path, i)
		src := d.path
		if i > 1 {
			src = fmt.Sprintf("%s.%d", d.path, i-1)
		}
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		_ = os.Remove(dst)
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	return nil
}
