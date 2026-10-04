package payload

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	ShapeClaude  = "claude"
	ShapeCopilot = "copilot"
	ShapeUnknown = "unknown"
)

type envelope struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	SessionID     string          `json:"session_id"`
	ToolUseID     string          `json:"tool_use_id"`

	CSessionID  string          `json:"sessionId"`
	CToolName   string          `json:"toolName"`
	CToolArgs   json.RawMessage `json:"toolArgs"`
	CToolCallID string          `json:"toolCallId"`
	CToolUseID  string          `json:"toolUseId"`
}

type Parsed struct {
	Shape     string
	Event     string
	ToolName  string
	SessionID string
	ToolUseID string
	IsSpawn   bool
	Derived   *Derived
}

type Derived struct {
	ArgKeys          []string `json:"arg_keys"`
	ArgsBytes        int      `json:"args_bytes"`
	ArgsWasString    bool     `json:"args_was_string,omitempty"`
	SubagentType     string   `json:"subagent_type,omitempty"`
	Description      string   `json:"description,omitempty"`
	PromptChars      int      `json:"prompt_chars"`
	PromptBytes      int      `json:"prompt_bytes"`
	DescriptionChars int      `json:"description_chars"`
	ModelPresent     bool     `json:"model_present"`
	Model            string   `json:"model,omitempty"`
}

var spawnTools = map[string]bool{"Agent": true, "Task": true, "task": true}

var subagentTypeKeys = []string{"subagent_type", "agent_type", "agentType", "subagentType"}

func IsSpawnTool(name string) bool { return spawnTools[name] }

func Parse(raw []byte) Parsed {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Parsed{Shape: ShapeUnknown}
	}
	p := Parsed{Event: env.HookEventName}
	var args json.RawMessage
	switch {
	case env.ToolName != "":
		p.Shape, p.ToolName, p.SessionID, p.ToolUseID, args = ShapeClaude, env.ToolName, env.SessionID, env.ToolUseID, env.ToolInput
	case env.CToolName != "":
		tuid := env.CToolCallID
		if tuid == "" {
			tuid = env.CToolUseID
		}
		p.Shape, p.ToolName, p.SessionID, p.ToolUseID, args = ShapeCopilot, env.CToolName, env.CSessionID, tuid, env.CToolArgs
	default:
		p.Shape = ShapeUnknown
		return p
	}
	p.IsSpawn = IsSpawnTool(p.ToolName)
	if p.IsSpawn {
		p.Derived = derive(args)
	}
	return p
}

func derive(args json.RawMessage) *Derived {
	d := &Derived{ArgKeys: []string{}}
	if len(args) == 0 {
		return d
	}
	if s, ok := unwrapString(args); ok {
		d.ArgsWasString = true
		args = s
	}
	d.ArgsBytes = len(args)
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return d
	}
	for k := range m {
		d.ArgKeys = append(d.ArgKeys, k)
	}
	sort.Strings(d.ArgKeys)
	if prompt, ok := m["prompt"].(string); ok {
		d.PromptChars = utf8.RuneCountInString(prompt)
		d.PromptBytes = len(prompt)
	}
	if desc, ok := m["description"].(string); ok {
		d.Description = desc
		d.DescriptionChars = utf8.RuneCountInString(desc)
	}
	d.SubagentType = firstString(m, subagentTypeKeys)
	if v, ok := m["model"]; ok {
		d.ModelPresent = true
		if s, ok := v.(string); ok {
			d.Model = s
		}
	}
	return d
}

type ModelKind int

const (
	ModelAbsent ModelKind = iota
	ModelString
	ModelInvalid
)

type ModelField struct {
	Kind  ModelKind
	Value string
}

type Spawn struct {
	Shape         string
	SessionID     string
	ToolUseID     string
	ToolName      string
	Args          map[string]json.RawMessage
	ArgsWasString bool
	Model         ModelField
	SubagentType  string
	Description   string
	Prompt        string
}

func ExtractSpawn(raw []byte) (Spawn, bool) {
	var env envelope
	if json.Unmarshal(raw, &env) != nil {
		return Spawn{}, false
	}
	var sp Spawn
	var args json.RawMessage
	switch {
	case env.ToolName != "":
		sp.Shape, sp.SessionID, sp.ToolUseID, sp.ToolName, args = ShapeClaude, env.SessionID, env.ToolUseID, env.ToolName, env.ToolInput
	case env.CToolName != "":
		tuid := env.CToolCallID
		if tuid == "" {
			tuid = env.CToolUseID
		}
		sp.Shape, sp.SessionID, sp.ToolUseID, sp.ToolName, args = ShapeCopilot, env.CSessionID, tuid, env.CToolName, env.CToolArgs
	default:
		return Spawn{}, false
	}
	if !IsSpawnTool(sp.ToolName) || len(args) == 0 {
		return Spawn{}, false
	}
	if s, ok := unwrapString(args); ok {
		sp.ArgsWasString = true
		args = s
	}
	if json.Unmarshal(args, &sp.Args) != nil {
		return Spawn{}, false
	}
	sp.Model = readModel(sp.Args)
	sp.SubagentType = firstRawString(sp.Args, subagentTypeKeys)
	sp.Description = rawString(sp.Args["description"])
	sp.Prompt = rawString(sp.Args["prompt"])
	return sp, true
}

func ExtractSpawnArgs(raw []byte) map[string]any {
	sp, ok := ExtractSpawn(raw)
	if !ok {
		return nil
	}
	b, err := json.Marshal(sp.Args)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func SetModel(args map[string]json.RawMessage, model string) ([]byte, error) {
	modelValue, err := jsonStringNoEscape(model)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(args)+1)
	seenModel := false
	for k := range args {
		keys = append(keys, k)
		if k == "model" {
			seenModel = true
		}
	}
	if !seenModel {
		keys = append(keys, "model")
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := jsonStringNoEscape(k)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		if k == "model" {
			b.Write(modelValue)
			continue
		}
		b.Write(args[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func jsonStringNoEscape(s string) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(b.String(), "\n")), nil
}

func unwrapString(args json.RawMessage) (json.RawMessage, bool) {
	t := bytes.TrimLeft(args, " \t\r\n")
	if len(t) == 0 || t[0] != '"' {
		return args, false
	}
	var s string
	if json.Unmarshal(t, &s) != nil {
		return args, false
	}
	return json.RawMessage(s), true
}

func readModel(args map[string]json.RawMessage) ModelField {
	raw, ok := args["model"]
	if !ok {
		return ModelField{Kind: ModelAbsent}
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return ModelField{Kind: ModelInvalid}
	}
	return ModelField{Kind: ModelString, Value: s}
}

func firstString(m map[string]any, keys []string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return ""
}

func firstRawString(m map[string]json.RawMessage, keys []string) string {
	for _, k := range keys {
		if s := rawString(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func rawString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}
