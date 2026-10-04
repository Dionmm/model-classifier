package main

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// The documented hook configs must stay valid JSON.
func TestDocumentedConfigsAreValidJSON(t *testing.T) {
	for _, name := range []string{"copilot-hooks.json", "claude-settings.json"} {
		b, err := os.ReadFile("../../examples/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(b) {
			t.Errorf("examples/%s is not valid JSON", name)
		}
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllSubmatch(readme, -1)
	if len(blocks) == 0 {
		t.Fatal("no json blocks found in README.md")
	}
	for i, m := range blocks {
		if !json.Valid(m[1]) {
			t.Errorf("README json block %d is invalid:\n%s", i, m[1])
		}
	}
}
