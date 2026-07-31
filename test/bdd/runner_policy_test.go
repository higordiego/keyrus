package bdd_test

import (
	"fmt"
	"testing"

	"github.com/cucumber/godog"
	"github.com/higordiegoti/keyrus/internal/bddrunner"
)

const fixtureFeature = `# language: pt
@SCN-FIXTURE-001
Funcionalidade: Política do runner
  Cenário: Executar binding selecionado
    Dado um efeito observável
`

const multiTagFixtureFeature = `# language: pt
Funcionalidade: Política multi-tag do runner
  @SCN-FIXTURE-001
  Cenário: Executar primeiro binding selecionado
    Dado o primeiro efeito observável

  @SCN-FIXTURE-002
  Cenário: Executar segundo binding selecionado
    Dado o segundo efeito observável
`

func fixtureConfig(initialize func(*godog.ScenarioContext)) bddrunner.Config {
	return bddrunner.Config{
		Name: "runner-policy-fixture",
		FeatureContents: []godog.Feature{{
			Name:     "runner-policy.feature",
			Contents: []byte(fixtureFeature),
		}},
		Tags:       []string{"@SCN-FIXTURE-001"},
		Initialize: initialize,
	}
}

func TestRunnerRejectsManifestTagWithoutBinding(t *testing.T) {
	err := bddrunner.Run(fixtureConfig(func(*godog.ScenarioContext) {}))
	if err == nil {
		t.Fatal("tag selected by the manifest passed without a binding")
	}
}

func TestRunnerAcceptsRealBindingWithObservableEffect(t *testing.T) {
	effects := 0
	err := bddrunner.Run(fixtureConfig(func(ctx *godog.ScenarioContext) {
		ctx.Step(`^um efeito observável$`, func() error {
			effects++
			if effects != 1 {
				return fmt.Errorf("unexpected effects: %d", effects)
			}
			return nil
		})
	}))
	if err != nil {
		t.Fatal(err)
	}
	if effects != 1 {
		t.Fatalf("observable effect count: got %d, want 1", effects)
	}
}

func TestRunnerUsesGodogLegacyORForMultipleManifestTags(t *testing.T) {
	effects := 0
	err := bddrunner.Run(bddrunner.Config{
		Name: "runner-multi-tag-fixture",
		FeatureContents: []godog.Feature{{
			Name:     "runner-multi-tag.feature",
			Contents: []byte(multiTagFixtureFeature),
		}},
		Tags: []string{"@SCN-FIXTURE-001", "@SCN-FIXTURE-002"},
		Initialize: func(ctx *godog.ScenarioContext) {
			ctx.Step(`^o primeiro efeito observável$`, func() error {
				effects++
				return nil
			})
			ctx.Step(`^o segundo efeito observável$`, func() error {
				effects++
				return nil
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if effects != 2 {
		t.Fatalf("observable effect count: got %d, want 2", effects)
	}
}

func TestRunnerRejectsPending(t *testing.T) {
	err := bddrunner.Run(fixtureConfig(func(ctx *godog.ScenarioContext) {
		ctx.Step(`^um efeito observável$`, func() error { return godog.ErrPending })
	}))
	if err == nil {
		t.Fatal("pending step was treated as success")
	}
}

func TestRunnerRejectsSkip(t *testing.T) {
	err := bddrunner.Run(fixtureConfig(func(ctx *godog.ScenarioContext) {
		ctx.Step(`^um efeito observável$`, func() error { return godog.ErrSkip })
	}))
	if err == nil {
		t.Fatal("skipped step was treated as success")
	}
}
