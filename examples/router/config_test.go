package router_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Dionmm/model-classifier/internal/daemon"
	"github.com/Dionmm/model-classifier/internal/jev"
)

func TestHookExamplesMatchPhase3Client(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		b, err := os.ReadFile("claude-settings.json")
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Hooks map[string][]struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		pre := cfg.Hooks["PreToolUse"][0]
		if pre.Matcher != "Agent|Task" || pre.Hooks[0].Timeout != 3 || !strings.Contains(pre.Hooks[0].Command, "--harness claude") {
			t.Fatalf("claude example mismatch: %#v", pre)
		}
	})
	t.Run("copilot", func(t *testing.T) {
		b, err := os.ReadFile("copilot-hooks.json")
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Version int `json:"version"`
			Hooks   struct {
				PreToolUse []struct {
					Matcher    string `json:"matcher"`
					Bash       string `json:"bash"`
					TimeoutSec int    `json:"timeoutSec"`
				} `json:"preToolUse"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		h := cfg.Hooks.PreToolUse[0]
		if cfg.Version != 1 || h.Matcher != "task" || h.TimeoutSec != 3 || !strings.Contains(h.Bash, "--harness copilot") {
			t.Fatalf("copilot example mismatch: %#v", cfg)
		}
	})
}

func TestConfigExampleLoadsThroughDaemonConfig(t *testing.T) {
	cfg, err := daemon.LoadConfig("config.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Harnesses["claude"].Enabled || cfg.Harnesses["copilot"].Enabled {
		t.Fatalf("example config should default both harnesses disabled: %#v", cfg.Harnesses)
	}
	if cfg.JevEndpoint != jev.DefaultEndpoint {
		t.Fatalf("jev_endpoint = %q, want default %q", cfg.JevEndpoint, jev.DefaultEndpoint)
	}
	if err := cfg.Config.Validate(); err != nil {
		t.Fatal(err)
	}
}
