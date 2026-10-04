package telemetry

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestExportFailureLogRateLimit(t *testing.T) {
	var buf bytes.Buffer
	oldWriter, oldNow := errorWriter, nowUnix
	oldLast := lastErrorLog.Load()
	oldFailures := exportFailures.Load()
	t.Cleanup(func() {
		errorWriter = oldWriter
		nowUnix = oldNow
		lastErrorLog.Store(oldLast)
		exportFailures.Store(oldFailures)
	})
	errorWriter = &buf
	now := int64(1000)
	nowUnix = func() int64 { return now }
	lastErrorLog.Store(0)
	exportFailures.Store(0)

	handleExportError(errors.New("first"))
	handleExportError(errors.New("second"))
	now += 59
	handleExportError(errors.New("third"))
	if got := strings.Count(buf.String(), "otel export failure"); got != 1 {
		t.Fatalf("export failure logs before 60s = %d, want 1; output=%q", got, buf.String())
	}
	now++
	handleExportError(errors.New("fourth"))
	if got := strings.Count(buf.String(), "otel export failure"); got != 2 {
		t.Fatalf("export failure logs after 60s = %d, want 2; output=%q", got, buf.String())
	}
	if exportFailures.Load() != 4 {
		t.Fatalf("export failures = %d, want 4", exportFailures.Load())
	}
}
