package spike

import (
	"bytes"
	"encoding/json"
)

// unwrapString decodes args when it is a JSON string, returning its contents.
// Only a leading quote counts: json.Unmarshal also accepts `null` into a
// string, which would misreport null args as string-encoded.
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
