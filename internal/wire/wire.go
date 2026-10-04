package wire

import (
	"os"
	"path/filepath"
	"time"
)

const (
	ProtocolVersion = "1"

	HeaderProtocol = "X-Router-Protocol"
	HeaderHarness  = "X-Router-Harness"
	HeaderDeadline = "X-Router-Deadline"

	PathRoute  = "/v1/route"
	PathVerify = "/v1/verify"
	PathHealth = "/v1/health"

	MaxPayloadBytes = 4 << 20
)

type VerifyRequest struct {
	Harness string `json:"harness"`
	Action  string `json:"action"`
}

type VerifyResponse struct {
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}

type HealthResponse struct {
	Protocol     int             `json:"protocol"`
	Version      string          `json:"version"`
	Harnesses    map[string]bool `json:"harnesses"`
	BreakerState string          `json:"breaker_state"`
	HasAPIKey    bool            `json:"has_api_key"`
}

func SocketPath() string {
	return filepath.Join(homeDir(), ".model-router", "run", "router.sock")
}

func DecisionsPath() string {
	return filepath.Join(homeDir(), ".model-router", "decisions.jsonl")
}

func ConfigPath() string {
	return filepath.Join(homeDir(), ".config", "model-router", "config.json")
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}
