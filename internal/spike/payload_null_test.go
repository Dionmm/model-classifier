package spike

import "testing"

func TestNullArgsAreNotReportedAsString(t *testing.T) {
	for _, raw := range []string{
		`{"toolName":"task","toolArgs":null}`,
		`{"tool_name":"Agent","tool_input":null}`,
	} {
		p := Parse([]byte(raw))
		if !p.IsSpawn || p.Derived == nil {
			t.Fatalf("%s: not parsed as spawn: %+v", raw, p)
		}
		if p.Derived.ArgsWasString {
			t.Errorf("%s: null args reported as JSON string", raw)
		}
		if ExtractSpawnArgs([]byte(raw)) != nil {
			t.Errorf("%s: null args extracted as a map", raw)
		}
	}
}
