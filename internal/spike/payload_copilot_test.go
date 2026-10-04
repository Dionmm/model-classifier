package spike

import "testing"

func TestParseCopilotSpawnObjectAndStringArgs(t *testing.T) {
	cases := map[string]string{
		"object": `{"prompt":"do x","agent_type":"explore"}`,
		"string": `"{\"prompt\":\"do x\",\"agent_type\":\"explore\"}"`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			p := Parse([]byte(`{"sessionId":"c1","timestamp":1,"cwd":"/x","toolName":"task","toolArgs":` + args + `}`))
			if p.Shape != ShapeCopilot || !p.IsSpawn || p.SessionID != "c1" {
				t.Fatalf("envelope: %+v", p)
			}
			d := p.Derived
			if d.PromptChars != 4 || d.SubagentType != "explore" || d.ModelPresent {
				t.Errorf("derived: %+v", d)
			}
			if d.ArgsWasString != (name == "string") {
				t.Errorf("ArgsWasString = %v", d.ArgsWasString)
			}
		})
	}
}

func TestExtractSpawnArgs(t *testing.T) {
	m := ExtractSpawnArgs([]byte(`{"tool_name":"Task","tool_input":{"prompt":"p"}}`))
	if m["prompt"] != "p" {
		t.Errorf("claude Task args: %v", m)
	}
	m = ExtractSpawnArgs([]byte(`{"toolName":"task","toolArgs":"{\"prompt\":\"q\"}"}`))
	if m["prompt"] != "q" {
		t.Errorf("copilot string args: %v", m)
	}
	if ExtractSpawnArgs([]byte(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`)) != nil {
		t.Error("non-spawn returned args")
	}
}
