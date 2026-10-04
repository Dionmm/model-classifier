package spike

import (
	"github.com/Dionmm/model-classifier/internal/payload"
)

const (
	ShapeClaude  = payload.ShapeClaude
	ShapeCopilot = payload.ShapeCopilot
	ShapeUnknown = payload.ShapeUnknown
)

type Parsed = payload.Parsed
type Derived = payload.Derived

var subagentTypeKeys = []string{"subagent_type", "agent_type", "agentType", "subagentType"}

func Parse(raw []byte) Parsed { return payload.Parse(raw) }

func ExtractSpawnArgs(raw []byte) map[string]any { return payload.ExtractSpawnArgs(raw) }
