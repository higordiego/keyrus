package bdd_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/higordiegoti/keyrus/internal/bddcatalog"
	"github.com/higordiegoti/keyrus/internal/bddguard"
	"github.com/higordiegoti/keyrus/internal/bddrunner"
	"github.com/higordiegoti/keyrus/test/bdd/steps"
	"github.com/higordiegoti/keyrus/test/support/runtimeevidence"
)

const (
	featuresDir = "../../features"
	manifest    = "../../features/implemented_scenarios.txt"
)

func TestFeatureCatalog(t *testing.T) {
	t.Parallel()
	catalog, err := bddcatalog.Load(featuresDir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(catalog.FeatureFiles), 14; got != want {
		t.Fatalf("feature file count: got %d, want %d", got, want)
	}
}

func TestAllFeaturesParseWithGodog(t *testing.T) {
	t.Parallel()
	status := godog.TestSuite{
		Name:                "gherkin-parser",
		ScenarioInitializer: func(*godog.ScenarioContext) {},
		Options: &godog.Options{
			Format:   "progress",
			NoColors: true,
			Strict:   true,
			Tags:     "@__parser_only_no_scenario_has_this_tag__",
			Paths:    []string{featuresDir},
			Output:   io.Discard,
		},
	}.Run()
	if status != 0 {
		t.Fatalf("Godog parser returned status %d", status)
	}
}

func TestImplementedScenarios(t *testing.T) {
	ensureRuntimeEvidence(t)
	catalog, err := bddcatalog.Load(featuresDir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := bddguard.ValidateStepSources("steps"); err != nil {
		t.Fatal(err)
	}
	runErr := bddrunner.Run(bddrunner.Config{
		Name:       "implemented-bdd",
		Paths:      []string{featuresDir},
		Tags:       catalog.ImplementedScenarios,
		Initialize: steps.Initialize,
	})
	if len(catalog.ImplementedScenarios) == 0 {
		if runErr == nil || !strings.Contains(runErr.Error(), "selection is empty") {
			t.Fatalf("empty manifest did not exercise the runner's non-vacuous guard: %v", runErr)
		}
		t.Log("manifest is empty and runner wiring rejected vacuous execution as required")
	} else if runErr != nil {
		t.Fatal(runErr)
	}
}

func ensureRuntimeEvidence(t *testing.T) {
	t.Helper()
	if path := os.Getenv("CASHFLOW_RUNTIME_EVIDENCE_FILE"); path != "" {
		if _, err := runtimeevidence.Load(path); err == nil {
			return
		}
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime-evidence.json")
	command := exec.Command("go", "test", "-race", "-count=1", "-timeout", "30m", "-run", "^TestRealEdgeIdentityRuntime$", "./test/integration")
	command.Dir = root
	command.Env = append(os.Environ(), "CASHFLOW_RUNTIME_EVIDENCE_FILE="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute real runtime for Godog bindings: %v\n%s", err, output)
	}
	if _, err := runtimeevidence.Load(path); err != nil {
		t.Fatalf("validate runtime evidence: %v", err)
	}
	t.Setenv("CASHFLOW_RUNTIME_EVIDENCE_FILE", path)
}
