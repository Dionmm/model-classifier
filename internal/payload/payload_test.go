package payload

import (
	"encoding/json"
	"testing"
)

func TestExtractSpawnTriStateAndFields(t *testing.T) {
	for name, tc := range map[string]struct {
		raw       string
		kind      ModelKind
		value     string
		wasString bool
		toolUseID string
	}{
		"claude string": {
			raw:       `{"session_id":"s","tool_name":"Agent","tool_use_id":"tu","tool_input":{"subagent_type":"review","description":"d","prompt":"p","model":"opus"}}`,
			kind:      ModelString,
			value:     "opus",
			toolUseID: "tu",
		},
		"copilot absent string args": {
			raw:       `{"sessionId":"s","toolName":"task","toolCallId":"tc","toolArgs":"{\"agent_type\":\"research\",\"description\":\"d\",\"prompt\":\"p\"}"}`,
			kind:      ModelAbsent,
			wasString: true,
			toolUseID: "tc",
		},
		"copilot toolUseId fallback": {
			raw:       `{"sessionId":"s","toolName":"task","toolUseId":"tu2","toolArgs":{"agent_type":"research","description":"d","prompt":"p","model":null}}`,
			kind:      ModelInvalid,
			toolUseID: "tu2",
		},
		"empty string invalid": {
			raw:  `{"tool_name":"Task","tool_input":{"prompt":"p","model":""}}`,
			kind: ModelInvalid,
		},
		"non-string invalid": {
			raw:  `{"tool_name":"Task","tool_input":{"prompt":"p","model":123}}`,
			kind: ModelInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			sp, ok := ExtractSpawn([]byte(tc.raw))
			if !ok {
				t.Fatal("ExtractSpawn returned false")
			}
			if sp.Model.Kind != tc.kind || sp.Model.Value != tc.value {
				t.Fatalf("model = %#v, want kind %v value %q", sp.Model, tc.kind, tc.value)
			}
			if sp.ArgsWasString != tc.wasString {
				t.Fatalf("ArgsWasString = %v, want %v", sp.ArgsWasString, tc.wasString)
			}
			if sp.ToolUseID != tc.toolUseID {
				t.Fatalf("ToolUseID = %q, want %q", sp.ToolUseID, tc.toolUseID)
			}
		})
	}
}

func TestSetModelPreservesRawValues(t *testing.T) {
	args := map[string]json.RawMessage{
		"model":  json.RawMessage(`"sonnet"`),
		"big":    json.RawMessage(`12345678901234567890`),
		"nested": json.RawMessage(`{"z": 12345678901234567890, "arr": [null, {"k": "v"}]}`),
		"nil":    json.RawMessage(`null`),
		"str":    json.RawMessage(`"<file>&</file>\u2028"`),
	}
	got, err := SetModel(args, "opus")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"big":    `12345678901234567890`,
		"nested": `{"z": 12345678901234567890, "arr": [null, {"k": "v"}]}`,
		"nil":    `null`,
		"str":    `"<file>&</file>\u2028"`,
	} {
		if string(out[k]) != want {
			t.Fatalf("%s raw = %s, want byte-identical %s", k, out[k], want)
		}
	}
	if string(out["model"]) != `"opus"` {
		t.Fatalf("model = %s, want opus", out["model"])
	}
}

func TestSetModelOnStringEncodedToolArgs(t *testing.T) {
	sp, ok := ExtractSpawn([]byte(`{"toolName":"task","toolArgs":"{\"prompt\":\"p\",\"count\":12345678901234567890}"}`))
	if !ok || !sp.ArgsWasString {
		t.Fatalf("spawn = %+v ok=%v", sp, ok)
	}
	got, err := SetModel(sp.Args, "claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	if string(out["count"]) != `12345678901234567890` {
		t.Fatalf("count raw = %s, want large int preserved", out["count"])
	}
	if string(out["model"]) != `"claude-sonnet-5"` {
		t.Fatalf("model = %s", out["model"])
	}
}
