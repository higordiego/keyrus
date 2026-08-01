package bdd_test

import (
	"io"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/higordiegoti/keyrus/internal/bddcatalog"
	"github.com/higordiegoti/keyrus/internal/bddguard"
	"github.com/higordiegoti/keyrus/internal/bddrunner"
	"github.com/higordiegoti/keyrus/test/bdd/steps"
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
