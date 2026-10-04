package spike

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"strings"
)

// CollectSpawns returns the distinct spawn args from pre-tool-use records.
// The same spawn can be logged twice (e.g. Copilot firing both a native and a
// Claude-style config), so args are deduplicated by content.
func CollectSpawns(r io.Reader) ([]map[string]any, error) {
	seen := map[[32]byte]bool{}
	var out []map[string]any
	_, err := ReadRecords(r, func(rec Record) {
		if !strings.HasPrefix(strings.ToLower(rec.Event), "pre") || len(rec.Raw) == 0 {
			return
		}
		args := ExtractSpawnArgs(rec.Raw)
		if args == nil {
			return
		}
		key, err := json.Marshal([]any{BenchState(args), args["model"]})
		if err != nil {
			return
		}
		h := sha256.Sum256(key)
		if seen[h] {
			return
		}
		seen[h] = true
		out = append(out, args)
	})
	return out, err
}
