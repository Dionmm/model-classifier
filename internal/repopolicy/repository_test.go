package repopolicy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestModelRouterBinaryAvoidsHeavyStartupDeps(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/model-router")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	deps := "\n" + string(out)
	for _, forbidden := range []string{"net/http", "go.opentelemetry.io/otel/sdk"} {
		if strings.Contains(deps, "\n"+forbidden+"\n") {
			t.Fatalf("cmd/model-router depends on %s, which regresses hook startup cost", forbidden)
		}
	}
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReleaseGhCommandsPassExplicitRepo(t *testing.T) {
	workflow := readRepoFile(t, ".github", "workflows", "build-reusable.yml")
	for _, line := range strings.Split(workflow, "\n") {
		for _, segment := range strings.Split(line, "||") {
			if strings.Contains(segment, "gh release ") && !strings.Contains(segment, `--repo "$GITHUB_REPOSITORY"`) {
				t.Fatalf("gh release command lacks explicit repository: %s", strings.TrimSpace(segment))
			}
		}
	}
}

func TestGovulncheckUsesGoToolDirective(t *testing.T) {
	gomod := readRepoFile(t, "go.mod")
	if !strings.Contains(gomod, "tool golang.org/x/vuln/cmd/govulncheck") {
		t.Fatal("go.mod missing govulncheck tool directive")
	}
	for _, path := range []string{
		filepath.Join(".github", "workflows", "ci.yml"),
		filepath.Join(".github", "workflows", "build-reusable.yml"),
	} {
		workflow := readRepoFile(t, strings.Split(path, string(filepath.Separator))...)
		if strings.Contains(workflow, "go install golang.org/x/vuln/cmd/govulncheck") {
			t.Fatalf("%s still hand-installs govulncheck", path)
		}
		if !strings.Contains(workflow, "go tool govulncheck") {
			t.Fatalf("%s does not invoke govulncheck through go tool", path)
		}
	}
}

func TestRenovateExplicitlyUpdatesGovulncheckToolDependency(t *testing.T) {
	var cfg struct {
		PackageRules []struct {
			MatchManagers     []string `json:"matchManagers"`
			MatchPackageNames []string `json:"matchPackageNames"`
			Enabled           *bool    `json:"enabled"`
		} `json:"packageRules"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "renovate.json")), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, rule := range cfg.PackageRules {
		if contains(rule.MatchManagers, "gomod") && contains(rule.MatchPackageNames, "golang.org/x/vuln") && rule.Enabled != nil && *rule.Enabled {
			return
		}
	}
	t.Fatal("renovate.json missing enabled gomod packageRule for golang.org/x/vuln tool dependency")
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestSupplyChainVerifyCommandPinsSourceRefAndTagRuleset(t *testing.T) {
	doc := readRepoFile(t, "docs", "supply-chain.md")
	for _, want := range []string{"--source-ref refs/tags/vX.Y.Z", "protect `v*` tags with a ruleset"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("supply-chain docs missing %q", want)
		}
	}
}
