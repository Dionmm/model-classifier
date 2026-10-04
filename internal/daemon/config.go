package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Dionmm/model-classifier/internal/jev"
	"github.com/Dionmm/model-classifier/internal/router"
	"github.com/Dionmm/model-classifier/internal/wire"
)

const (
	Version        = "phase2"
	DefaultModel   = "jev-1.13.0"
	DefaultTimeout = 1200 * time.Millisecond
	MinTimeoutMS   = 1
	MaxTimeoutMS   = 1400
)

type Config struct {
	JevEndpoint string `json:"jev_endpoint"`
	PinnedModel string `json:"pinned_model"`
	TimeoutMS   int    `json:"timeout_ms"`
	Questions   string `json:"questions_path"`
	APIKeyFile  string `json:"api_key_file"`

	router.Config
}

func DefaultConfig() Config {
	return Config{
		JevEndpoint: jev.DefaultEndpoint,
		PinnedModel: DefaultModel,
		TimeoutMS:   int(DefaultTimeout / time.Millisecond),
		Config:      router.DefaultConfig(),
	}
}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		path = wire.ConfigPath()
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, err
	}
	cfg := DefaultConfig()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.JevEndpoint == "" {
		cfg.JevEndpoint = jev.DefaultEndpoint
	}
	if cfg.PinnedModel == "" {
		cfg.PinnedModel = DefaultModel
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Config.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateConfig(cfg Config) error {
	if cfg.TimeoutMS < MinTimeoutMS || cfg.TimeoutMS > MaxTimeoutMS {
		return fmt.Errorf("timeout_ms must be between %d and %d milliseconds", MinTimeoutMS, MaxTimeoutMS)
	}
	return nil
}

func (c Config) Timeout() time.Duration {
	if c.TimeoutMS <= 0 {
		return DefaultTimeout
	}
	return time.Duration(c.TimeoutMS) * time.Millisecond
}
