//go:build unix

package spike

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Same-process O_APPEND writes are already atomic on local filesystems, so
// the concurrency test can't prove the lock. This checks it directly: an
// append must wait while another descriptor holds the lock.
func TestAppendLineWaitsForLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	holder, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := lockFile(holder); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- AppendLine(path, []byte("x")) }()
	select {
	case err := <-done:
		t.Fatalf("AppendLine returned (%v) while the file was locked", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := unlockFile(holder); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AppendLine still blocked after unlock")
	}
}
