package telemetry

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecisionLogFlushesWhenIdleBeforeClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	d, err := NewDecisionLog(path, 1<<20, 3, 16, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	d.Record(DecisionLine{Harness: "claude", ToolUseID: "tu-idle", Reason: "override"})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), `"tool_use_id":"tu-idle"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := os.ReadFile(path)
	t.Fatalf("line not written before Close: %s", b)
}

func TestDecisionLogCloseDrainsQueuedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	const lines = 4096
	d, err := NewDecisionLog(path, 1<<20, 3, lines, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < lines; i++ {
		d.Record(DecisionLine{TS: time.Unix(int64(i), 0), Harness: "claude", Reason: "drain", StateBytes: i})
	}
	if dropped := d.Dropped(); dropped != 0 {
		t.Fatalf("queued lines dropped = %d, want 0", dropped)
	}
	_ = d.Close()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), `"reason":"drain"`); got != lines {
		t.Fatalf("drained decision lines = %d, want %d; log=%s", got, lines, b)
	}
}

func TestDecisionLogRotationCountsBufferedBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	const (
		lines    = 15
		maxBytes = 90
	)
	d := &DecisionLog{path: path, maxBytes: maxBytes, keep: 3, ch: make(chan []byte, lines), done: make(chan struct{}), metrics: Metrics{}}
	for i := 0; i < lines; i++ {
		d.ch <- []byte(strings.Repeat(string(rune('a'+i)), 20) + "\n")
	}
	close(d.ch)
	d.run()

	var combined strings.Builder
	for _, p := range []string{path + ".3", path + ".2", path + ".1", path} {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(b)) > maxBytes {
			t.Fatalf("rotation must count buffered bytes: %s size=%d exceeds maxBytes=%d", filepath.Base(p), len(b), maxBytes)
		}
		combined.Write(b)
	}
	for i := 0; i < lines; i++ {
		line := strings.Repeat(string(rune('a'+i)), 20) + "\n"
		if got := strings.Count(combined.String(), line); got != 1 {
			t.Fatalf("rotation must preserve buffered lines: line %q count=%d, want 1; combined=%q", line, got, combined.String())
		}
	}
}

func TestDecisionLogReopensAfterTransientWriterFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	var failed atomic.Bool
	d, err := NewDecisionLog(path, 1<<20, 3, 4, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	d.openFile = func() (logFile, error) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if !failed.Load() {
			return &failOnceFile{File: f, failed: &failed}, nil
		}
		return f, nil
	}
	d.Record(DecisionLine{ToolUseID: "first-dropped", Reason: "transient"})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && d.Dropped() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if d.Dropped() != 1 {
		t.Fatalf("transient writer failure drops = %d, want 1", d.Dropped())
	}
	d.Record(DecisionLine{ToolUseID: "second-written", Reason: "transient"})
	_ = d.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"tool_use_id":"second-written"`) {
		t.Fatalf("decision log did not reopen after transient writer failure mechanism; log=%s", b)
	}
	if strings.Contains(string(b), `"tool_use_id":"first-dropped"`) {
		t.Fatalf("failed line unexpectedly written after transient writer failure: %s", b)
	}
}

func TestDecisionLogRecordAfterCloseDropsWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDecisionLog(filepath.Join(dir, "decisions.jsonl"), 1<<20, 3, 1, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d.Record(DecisionLine{Reason: "after_close"})
	if d.Dropped() != 1 {
		t.Fatalf("record after close drops = %d, want 1", d.Dropped())
	}
}

func TestDecisionLogRotationPermsAndFullChannelDrop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "decisions.jsonl")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	d, err := NewDecisionLog(path, 80, 3, 4, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		d.Record(DecisionLine{TS: time.Unix(int64(i), 0), Harness: "claude", Reason: "override", StateBytes: i})
	}
	_ = d.Close()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("decision log mode = %o, want 0600", st.Mode().Perm())
	}
	for i := 1; i <= 3; i++ {
		if _, err := os.Stat(path + "." + string(rune('0'+i))); err != nil {
			t.Fatalf("missing rotated log %d: %v", i, err)
		}
	}

	blocked := &DecisionLog{path: filepath.Join(dir, "blocked.jsonl"), maxBytes: 1 << 20, keep: 3, ch: make(chan []byte, 1), done: make(chan struct{})}
	blocked.ch <- []byte("parked\n")
	blocked.Record(DecisionLine{Reason: "full"})
	if blocked.Dropped() != 1 {
		t.Fatalf("full channel drops = %d, want 1", blocked.Dropped())
	}
}

type failOnceFile struct {
	*os.File
	failed *atomic.Bool
}

func (f *failOnceFile) Write([]byte) (int, error) {
	if f.failed.CompareAndSwap(false, true) {
		return 0, errors.New("transient write failure")
	}
	return f.File.Write(nil)
}

func (f *failOnceFile) Stat() (fs.FileInfo, error) {
	return f.File.Stat()
}

// writeFailFile fails every Write; Stat and the embedded file are real.
type writeFailFile struct{ *os.File }

func (f *writeFailFile) Write([]byte) (int, error) { return 0, errors.New("disk write failure") }

func waitDropped(t *testing.T, d *DecisionLog, want uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && d.Dropped() < want {
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDecisionLogReopensAfterWriteCallFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	var opens atomic.Int32
	d, err := NewDecisionLog(path, 1<<20, 3, 4, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	d.openFile = func() (logFile, error) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if opens.Add(1) == 1 {
			return &writeFailFile{f}, nil
		}
		return f, nil
	}
	// Larger than the bufio buffer, so w.Write itself hits the failing file.
	d.Record(DecisionLine{ToolUseID: "huge-dropped", Reason: strings.Repeat("x", 8192)})
	waitDropped(t, d, 1)
	if d.Dropped() != 1 {
		t.Fatalf("Write failure drops = %d, want 1", d.Dropped())
	}
	d.Record(DecisionLine{ToolUseID: "after-reopen", Reason: "ok"})
	_ = d.Close()
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"tool_use_id":"after-reopen"`) {
		t.Fatalf("log did not reopen after w.Write failure (bufio error is sticky); log=%s", b)
	}
}

func TestDecisionLogFlushFailureDropsAllBufferedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	const n = 5
	gate := make(chan struct{})
	d, err := NewDecisionLog(path, 1<<20, 3, 16, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	d.openFile = func() (logFile, error) {
		<-gate
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if opens.Add(1) == 1 {
			return &writeFailFile{f}, nil
		}
		return f, nil
	}
	// The worker blocks opening the file while all n lines queue, so they are
	// buffered together and flushed (and lost) as one batch.
	for i := 0; i < n; i++ {
		d.Record(DecisionLine{ToolUseID: "buffered", Reason: "r"})
	}
	close(gate)
	waitDropped(t, d, n)
	_ = d.Close()
	if d.Dropped() != n {
		t.Fatalf("flush failure of %d buffered lines counted %d drops, want %d", n, d.Dropped(), n)
	}
}

func TestDecisionLogRotationFlushFailureCountsBufferedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	gate := make(chan struct{})
	d, err := NewDecisionLog(path, 100, 3, 16, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	d.openFile = func() (logFile, error) {
		if opens.Load() == 0 {
			<-gate
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if opens.Add(1) == 1 {
			return &writeFailFile{f}, nil
		}
		return f, nil
	}
	// Line 1 sits in the buffer; line 2 forces a rotation, whose closeFile(true)
	// flush fails and loses line 1.
	d.Record(DecisionLine{ToolUseID: "one", Reason: "r"})
	d.Record(DecisionLine{ToolUseID: "two", Reason: "r"})
	close(gate)
	_ = d.Close()
	if d.Dropped() != 1 {
		t.Fatalf("rotation flush failure drops = %d, want 1 (flush error in closeFile ignored)", d.Dropped())
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"tool_use_id":"two"`) {
		t.Fatalf("line after rotation missing; log=%s", b)
	}
}

func TestDecisionLogTerminatesPartialTailBeforeAppending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	const frag = `{"ts":"2026-01-01T00:00:00Z","reason":"trunc`
	if err := os.WriteFile(path, []byte(frag), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDecisionLog(path, 1<<20, 3, 4, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	d.Record(DecisionLine{ToolUseID: "intact", Reason: "ok"})
	_ = d.Close()
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 || lines[0] != frag {
		t.Fatalf("partial tail not isolated on its own line; log=%q", b)
	}
	if !strings.Contains(lines[1], `"tool_use_id":"intact"`) || !strings.HasPrefix(lines[1], `{"ts"`) {
		t.Fatalf("record after partial tail merged into fragment; log=%q", b)
	}
}

// scriptFile is a real file with hookable Write and Stat.
type scriptFile struct {
	*os.File
	onWrite func(p []byte) (int, error)
	onStat  func(n int)
	stats   int
}

func (f *scriptFile) Write(p []byte) (int, error) {
	if f.onWrite != nil {
		return f.onWrite(p)
	}
	return f.File.Write(p)
}

func (f *scriptFile) Stat() (fs.FileInfo, error) {
	f.stats++
	if f.onStat != nil {
		f.onStat(f.stats)
	}
	return f.File.Stat()
}

func openRW(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newTestLog(t *testing.T, path string, buf int, open func() (logFile, error)) *DecisionLog {
	t.Helper()
	d, err := NewDecisionLog(path, 1<<20, 3, buf, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	d.openFile = open
	return d
}

// A line too big for the buffer is written directly. The buffered lines must
// be flushed first, so a failure of the direct write costs only that line.
func TestDecisionLogDirectWriteFailureCountsOnlyTheBigLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	gate := make(chan struct{})
	var opens atomic.Int32
	d := newTestLog(t, path, 16, func() (logFile, error) {
		<-gate
		f := &scriptFile{File: openRW(t, path)}
		if opens.Add(1) == 1 {
			writes := 0
			f.onWrite = func(p []byte) (int, error) {
				writes++
				if writes == 1 {
					return f.File.Write(p)
				}
				return 0, errors.New("disk write failure")
			}
		}
		return f, nil
	})
	for i := 0; i < 3; i++ {
		d.Record(DecisionLine{ToolUseID: "small", Reason: "r"})
	}
	d.Record(DecisionLine{ToolUseID: "huge", Reason: strings.Repeat("x", 9000)})
	close(gate)
	waitDropped(t, d, 1)
	_ = d.Close()
	if d.Dropped() != 1 {
		t.Fatalf("drops = %d, want 1: the 3 small lines reached disk and only the direct write failed", d.Dropped())
	}
	b, _ := os.ReadFile(path)
	if got := strings.Count(string(b), `"tool_use_id":"small"`); got != 3 {
		t.Fatalf("small lines on disk = %d, want 3", got)
	}
}

// If the pre-write flush itself fails, the buffered lines and the big line are lost.
func TestDecisionLogPreWriteFlushFailureCountsBufferedAndCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	gate := make(chan struct{})
	var opens atomic.Int32
	d := newTestLog(t, path, 16, func() (logFile, error) {
		<-gate
		f := &scriptFile{File: openRW(t, path)}
		if opens.Add(1) == 1 {
			f.onWrite = func([]byte) (int, error) { return 0, errors.New("disk write failure") }
		}
		return f, nil
	})
	for i := 0; i < 3; i++ {
		d.Record(DecisionLine{ToolUseID: "small", Reason: "r"})
	}
	d.Record(DecisionLine{ToolUseID: "huge", Reason: strings.Repeat("x", 9000)})
	close(gate)
	waitDropped(t, d, 4)
	_ = d.Close()
	if d.Dropped() != 4 {
		t.Fatalf("drops = %d, want 4 (3 buffered + the big line)", d.Dropped())
	}
}

// After a successful idle flush nothing is pending, so a later failed batch
// counts only its own lines.
func TestDecisionLogPendingResetsAfterIdleFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	blocked := make(chan struct{})
	release := make(chan struct{})
	d := newTestLog(t, path, 16, func() (logFile, error) {
		f := &scriptFile{File: openRW(t, path)}
		writes := 0
		f.onWrite = func(p []byte) (int, error) {
			writes++
			if writes == 1 {
				return f.File.Write(p)
			}
			return 0, errors.New("disk write failure")
		}
		// Stat 1 is repairTail, 2 is line A, 3 is line B: hold B so C and D queue behind it.
		f.onStat = func(n int) {
			if n == 3 {
				close(blocked)
				<-release
			}
		}
		return f, nil
	})
	d.Record(DecisionLine{ToolUseID: "A", Reason: "r"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(path); strings.Contains(string(b), `"A"`) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.Record(DecisionLine{ToolUseID: "B", Reason: "r"})
	<-blocked
	d.Record(DecisionLine{ToolUseID: "C", Reason: "r"})
	d.Record(DecisionLine{ToolUseID: "D", Reason: "r"})
	close(release)
	waitDropped(t, d, 3)
	_ = d.Close()
	if d.Dropped() != 3 {
		t.Fatalf("drops = %d, want 3 (B, C, D); A was flushed and must not be pending", d.Dropped())
	}
}

// Reopening after a failure starts with nothing pending.
func TestDecisionLogPendingResetsInCloseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var opens atomic.Int32
	d := newTestLog(t, path, 16, func() (logFile, error) {
		n := int(opens.Add(1))
		if n > len(gates) {
			t.Errorf("unexpected open %d", n)
			return nil, errors.New("unexpected open")
		}
		<-gates[n-1]
		f := &scriptFile{File: openRW(t, path)}
		f.onWrite = func([]byte) (int, error) { return 0, errors.New("disk write failure") }
		return f, nil
	})
	for i := 0; i < 3; i++ {
		d.Record(DecisionLine{ToolUseID: "batch1", Reason: "r"})
	}
	close(gates[0])
	waitDropped(t, d, 3)
	for i := 0; i < 2; i++ {
		d.Record(DecisionLine{ToolUseID: "batch2", Reason: "r"})
	}
	close(gates[1])
	waitDropped(t, d, 5)
	_ = d.Close()
	if d.Dropped() != 5 {
		t.Fatalf("drops = %d, want 5 (3 + 2); stale pending carried across reopen", d.Dropped())
	}
}

func TestDecisionLogDoesNotAddBlankLineAfterCompleteTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	if err := os.WriteFile(path, []byte("{\"old\":1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDecisionLog(path, 1<<20, 3, 4, Metrics{})
	if err != nil {
		t.Fatal(err)
	}
	d.Record(DecisionLine{ToolUseID: "new", Reason: "ok"})
	_ = d.Close()
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "\n\n") || strings.Count(string(b), "\n") != 2 {
		t.Fatalf("repairTail added a blank line to a newline-terminated file: %q", b)
	}
}

func TestDecisionLogTailRepairWriteFailureDropsLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	const frag = `{"ts":"x","reason":"trunc`
	if err := os.WriteFile(path, []byte(frag), 0600); err != nil {
		t.Fatal(err)
	}
	var opens atomic.Int32
	d := newTestLog(t, path, 4, func() (logFile, error) {
		f := &scriptFile{File: openRW(t, path)}
		if opens.Add(1) == 1 {
			f.onWrite = func(p []byte) (int, error) {
				if len(p) == 1 {
					return 0, errors.New("repair write failure")
				}
				return f.File.Write(p)
			}
		}
		return f, nil
	})
	d.Record(DecisionLine{ToolUseID: "lost", Reason: "r"})
	waitDropped(t, d, 1)
	if d.Dropped() != 1 {
		t.Fatalf("drops = %d, want 1 when the tail repair fails", d.Dropped())
	}
	d.Record(DecisionLine{ToolUseID: "next", Reason: "r"})
	_ = d.Close()
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `"lost"`) {
		t.Fatalf("line was written after a failed tail repair, merging into the fragment: %q", b)
	}
	if !strings.Contains(string(b), "\n{") || !strings.Contains(string(b), `"next"`) {
		t.Fatalf("later line not repaired onto its own line: %q", b)
	}
}

func TestDecisionLogShortWriteThenReopenKeepsNextLineIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	var opens atomic.Int32
	d := newTestLog(t, path, 4, func() (logFile, error) {
		f := &scriptFile{File: openRW(t, path)}
		if opens.Add(1) == 1 {
			f.onWrite = func(p []byte) (int, error) {
				n, _ := f.File.Write(p[:len(p)/2])
				return n, errors.New("ENOSPC")
			}
		}
		return f, nil
	})
	d.Record(DecisionLine{ToolUseID: "half", Reason: "r"})
	waitDropped(t, d, 1)
	d.Record(DecisionLine{ToolUseID: "whole", Reason: "r"})
	_ = d.Close()
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], `{"ts"`) || !strings.HasSuffix(lines[1], `"reason":"r"}`) || !strings.Contains(lines[1], `"whole"`) {
		t.Fatalf("line after a short write is not intact on its own line: %q", b)
	}
}

// failSecondWrite lets the first Write through and fails every later one.
func failSecondWrite(f *scriptFile) {
	writes := 0
	f.onWrite = func(p []byte) (int, error) {
		writes++
		if writes == 1 {
			return f.File.Write(p)
		}
		return 0, errors.New("disk write failure")
	}
}

func recordBatchThenWait(t *testing.T, d *DecisionLog, gate chan struct{}, lines []DecisionLine, want uint64) {
	t.Helper()
	for _, l := range lines {
		d.Record(l)
	}
	close(gate)
	waitDropped(t, d, want)
	_ = d.Close()
}

// A line written directly by bufio (too big for the empty buffer) is already
// on disk, so it must not count as pending when a later flush fails.
func TestDecisionLogDirectWriteLineIsNotPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	gate := make(chan struct{})
	d := newTestLog(t, path, 16, func() (logFile, error) {
		<-gate
		f := &scriptFile{File: openRW(t, path)}
		failSecondWrite(f)
		return f, nil
	})
	recordBatchThenWait(t, d, gate, []DecisionLine{
		{ToolUseID: "huge", Reason: strings.Repeat("x", 9000)},
		{ToolUseID: "small", Reason: "r"},
	}, 1)
	if d.Dropped() != 1 {
		t.Fatalf("drops = %d, want 1: the direct-written big line is on disk, only the small line was lost", d.Dropped())
	}
}

// After a successful pre-write flush nothing is pending, so a later failed
// flush counts only the line that was buffered after it.
func TestDecisionLogPendingResetsAfterPreWriteFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	gate := make(chan struct{})
	d := newTestLog(t, path, 16, func() (logFile, error) {
		<-gate
		f := &scriptFile{File: openRW(t, path)}
		failSecondWrite(f)
		return f, nil
	})
	// ~4010 bytes: fits the 4096 buffer, but not the space left after 3 small lines.
	recordBatchThenWait(t, d, gate, []DecisionLine{
		{ToolUseID: "s1", Reason: "r"},
		{ToolUseID: "s2", Reason: "r"},
		{ToolUseID: "s3", Reason: "r"},
		{ToolUseID: "mid", Reason: strings.Repeat("x", 3950)},
	}, 1)
	if d.Dropped() != 1 {
		t.Fatalf("drops = %d, want 1: the 3 small lines were flushed by the pre-write flush", d.Dropped())
	}
}
