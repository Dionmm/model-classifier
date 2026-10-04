package spike

import "testing"

func TestParseClaudeSpawn(t *testing.T) {
	raw := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Agent","tool_use_id":"tu1",
	 "tool_input":{"prompt":"héllo","description":"find it","subagent_type":"Explore","model":"haiku"}}`
	p := Parse([]byte(raw))
	if p.Shape != ShapeClaude || !p.IsSpawn || p.Event != "PreToolUse" || p.SessionID != "s1" || p.ToolUseID != "tu1" {
		t.Fatalf("envelope: %+v", p)
	}
	d := p.Derived
	if d.PromptChars != 5 || d.PromptBytes != 6 {
		t.Errorf("prompt chars/bytes = %d/%d, want 5/6 (runes, not bytes)", d.PromptChars, d.PromptBytes)
	}
	if d.SubagentType != "Explore" || !d.ModelPresent || d.Model != "haiku" || d.DescriptionChars != 7 {
		t.Errorf("derived: %+v", d)
	}
	if len(d.ArgKeys) != 4 || d.ArgKeys[0] != "description" {
		t.Errorf("arg keys not sorted/complete: %v", d.ArgKeys)
	}
}

func TestParseNonSpawnAndGarbage(t *testing.T) {
	if p := Parse([]byte(`{"toolName":"bash","toolArgs":{"command":"ls"}}`)); p.IsSpawn || p.Derived != nil {
		t.Errorf("bash parsed as spawn: %+v", p)
	}
	for _, raw := range []string{"", "not json", "[1,2]", `{"x":1}`} {
		if p := Parse([]byte(raw)); p.IsSpawn || p.Shape != ShapeUnknown {
			t.Errorf("Parse(%q) = %+v", raw, p)
		}
	}
}
