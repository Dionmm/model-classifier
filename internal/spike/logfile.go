package spike

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PayloadsFile is the log file name inside the log directory.
const PayloadsFile = "payloads.jsonl"

// ErrorsFile collects hook errors so they never reach the harness.
const ErrorsFile = "errors.log"

// DefaultDir returns $MODEL_ROUTER_SPIKE_DIR or ~/.model-router-spike.
func DefaultDir() (string, error) {
	if d := os.Getenv("MODEL_ROUTER_SPIKE_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("no home directory")
	}
	return filepath.Join(home, ".model-router-spike"), nil
}

// AppendLine appends line plus a newline under an exclusive file lock, so
// concurrent hook processes never interleave records.
func AppendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return err
	}
	defer unlockFile(f)
	buf := make([]byte, 0, len(line)+1)
	buf = append(append(buf, line...), '\n')
	_, err = f.Write(buf)
	return err
}

// envPrefixes select env var names (never values) worth recording to tell
// harnesses apart.
var envPrefixes = []string{"CLAUDE", "COPILOT", "GITHUB_COPILOT"}

func envNames(environ []string) []string {
	names := []string{}
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		for _, p := range envPrefixes {
			if strings.HasPrefix(name, p) {
				names = append(names, name)
				break
			}
		}
	}
	sort.Strings(names)
	return names
}
