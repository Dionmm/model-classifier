package spike

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []Record {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var recs []Record
	bad, err := ReadRecords(f, func(r Record) { recs = append(recs, r) })
	if err != nil || bad != 0 {
		t.Fatalf("read: bad=%d err=%v", bad, err)
	}
	return recs
}

func TestRunHookLogsSpawnWithRawAndEnvNamesOnly(t *testing.T) {
	dir := t.TempDir()
	raw := `{"toolName":"task","toolArgs":{"prompt":"p"}}`
	err := RunHook(strings.NewReader(raw), HookOptions{
		Source: "copilot", Event: "preToolUse", Dir: dir,
		Now:     func() time.Time { return time.Unix(0, 0) },
		Environ: func() []string { return []string{"COPILOT_TOKEN=secret", "HOME=/h", "CLAUDE_PROJECT_DIR=/p"} },
	})
	if err != nil {
		t.Fatal(err)
	}
	recs := readLines(t, filepath.Join(dir, PayloadsFile))
	if len(recs) != 1 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.Source != "copilot" || r.Event != "preToolUse" || r.Shape != ShapeCopilot || r.Derived == nil {
		t.Errorf("record: %+v", r)
	}
	var got, want any
	_ = json.Unmarshal(r.Raw, &got)
	_ = json.Unmarshal([]byte(raw), &want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("raw payload not preserved: %s", r.Raw)
	}
	if strings.Join(r.EnvNames, ",") != "CLAUDE_PROJECT_DIR,COPILOT_TOKEN" {
		t.Errorf("env names = %v", r.EnvNames)
	}
	b, _ := os.ReadFile(filepath.Join(dir, PayloadsFile))
	if bytes.Contains(b, []byte("secret")) {
		t.Error("env var value leaked into log")
	}
}

func TestRunHookSkipsNonSpawnUnlessLogAll(t *testing.T) {
	dir := t.TempDir()
	raw := `{"tool_name":"Bash","tool_input":{"command":"ls"}}`
	if err := RunHook(strings.NewReader(raw), HookOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if n := len(readLines(t, filepath.Join(dir, PayloadsFile))); n != 0 {
		t.Fatalf("non-spawn logged %d records", n)
	}
	if err := RunHook(strings.NewReader(raw), HookOptions{Dir: dir, LogAll: true}); err != nil {
		t.Fatal(err)
	}
	if n := len(readLines(t, filepath.Join(dir, PayloadsFile))); n != 1 {
		t.Fatalf("log-all logged %d records", n)
	}
}

func TestRunHookConcurrentAppendsDoNotInterleave(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 256<<10) // well above any atomic-append size
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw := fmt.Sprintf(`{"tool_name":"Agent","tool_input":{"prompt":"%d-%s"}}`, i, big)
			if err := RunHook(strings.NewReader(raw), HookOptions{Dir: dir}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(readLines(t, filepath.Join(dir, PayloadsFile))); n != 32 {
		t.Fatalf("got %d intact records, want 32", n)
	}
}
