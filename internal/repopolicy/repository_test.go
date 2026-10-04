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

func workflowSteps(t *testing.T) []string {
	t.Helper()
	workflow := readRepoFile(t, ".github", "workflows", "build-reusable.yml")
	return strings.Split(workflow, "\n      - ")
}

func TestSbomActionDoesNotUploadItsOwnArtifactsOrReleaseAssets(t *testing.T) {
	n := 0
	for _, step := range workflowSteps(t) {
		if !strings.HasPrefix(step, "uses: anchore/sbom-action@") {
			continue
		}
		n++
		for _, want := range []string{`upload-artifact: "false"`, `upload-release-assets: "false"`} {
			if !strings.Contains(step, want) {
				t.Errorf("sbom-action step %d lacks %s, so it uploads stray files that publish-release would attach", n, want)
			}
		}
	}
	if n != 2 {
		t.Fatalf("found %d sbom-action steps, want 2", n)
	}
}

func TestReleaseDownloadIsLimitedToBuildArtifacts(t *testing.T) {
	for _, step := range workflowSteps(t) {
		if !strings.HasPrefix(step, "uses: actions/download-artifact@") {
			continue
		}
		if !strings.Contains(step, "pattern: model-router-*") {
			t.Fatal("download-artifact step lacks pattern: model-router-*, so unrelated workflow artifacts are downloaded")
		}
		if !strings.Contains(step, "merge-multiple: true") {
			t.Fatal("download-artifact step lacks merge-multiple: true")
		}
		return
	}
	t.Fatal("no download-artifact step found")
}

func TestReleaseUploadGlobsFilesNotDirectories(t *testing.T) {
	workflow := readRepoFile(t, ".github", "workflows", "build-reusable.yml")
	var upload string
	for _, line := range strings.Split(workflow, "\n") {
		if strings.Contains(line, "gh release upload") {
			upload = line
		}
	}
	if upload == "" {
		t.Fatal("no gh release upload command found")
	}
	for _, want := range []string{"release/dist/*", "release/sbom/*", "--clobber"} {
		if !strings.Contains(upload, want) {
			t.Errorf("gh release upload lacks %s: %s", want, strings.TrimSpace(upload))
		}
	}
	if strings.Contains(upload, " release/*") {
		t.Errorf("gh release upload globs release/*, which matches the dist and sbom directories: %s", strings.TrimSpace(upload))
	}
	if !strings.Contains(workflow, "shopt -s failglob") {
		t.Error("release upload lacks shopt -s failglob, so an empty glob would not fail")
	}
}
