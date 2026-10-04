package launchd_test

import (
	"bytes"
	"encoding/xml"
	"os"
	"testing"
)

func TestModelRouterdPlistHasLaunchdKeys(t *testing.T) {
	b, err := os.ReadFile("com.dionmm.model-routerd.plist")
	if err != nil {
		t.Fatal(err)
	}
	if err := xml.NewDecoder(bytes.NewReader(b)).Decode(new(any)); err != nil {
		t.Fatalf("plist is not XML: %v", err)
	}
	for _, key := range []string{"ProgramArguments", "RunAtLoad", "KeepAlive", "StandardErrorPath", "EnvironmentVariables", "OTEL_EXPORTER_OTLP_ENDPOINT"} {
		if !bytes.Contains(b, []byte("<key>"+key+"</key>")) {
			t.Fatalf("plist missing key %s", key)
		}
	}
}
