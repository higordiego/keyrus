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
			Format:   "pretty",
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
	evidence := realRuntimeEvidence(t)
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
		Initialize: steps.Initialize(evidence),
		Randomize:  20260801,
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

// realRuntimeEvidence owns the whole lifecycle of the proof. If this process
// already inherited a file path and a key, meaning `make test`/`make ci`
// minted that key once and used it for the direct E2E run that precedes this
// suite, and the file at that path attests under that same key, it is
// reused so the gate does not start a second real stack. Any other file left
// at that path, or a key that does not match, fails the keyed attestation and
// falls through: this process mints its own key and starts the real container
// stack itself, so a pre-written file can never reach these bindings on its
// own.
func realRuntimeEvidence(t *testing.T) runtimeevidence.Evidence {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if inherited, ok := inheritedRuntimeEvidence(t, root); ok {
		return inherited
	}
	keyPath, key, cleanup, err := runtimeevidence.WriteKeyFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	path := filepath.Join(t.TempDir(), "runtime-evidence.json")
	command := exec.Command("go", "test", "-race", "-count=1", "-timeout", "30m",
		"-run", "^TestRealEdgeIdentityRuntime$", "./test/integration")
	command.Dir = root
	command.Env = append(os.Environ(),
		runtimeevidence.FileEnvVar+"="+path,
		runtimeevidence.KeyFileEnvVar+"="+keyPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute real runtime for Godog bindings: %v\n%s", err, output)
	}
	evidence, err := runtimeevidence.LoadForSource(path, root, key)
	if err != nil {
		t.Fatalf("validate runtime evidence: %v", err)
	}
	return evidence
}

func inheritedRuntimeEvidence(t *testing.T, root string) (runtimeevidence.Evidence, bool) {
	t.Helper()
	path := os.Getenv(runtimeevidence.FileEnvVar)
	if path == "" {
		return runtimeevidence.Evidence{}, false
	}
	key, err := runtimeevidence.KeyFromEnv()
	if err != nil {
		return runtimeevidence.Evidence{}, false
	}
	evidence, err := runtimeevidence.LoadForSource(path, root, key)
	if err != nil {
		return runtimeevidence.Evidence{}, false
	}
	return evidence, true
}
