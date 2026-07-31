package bdd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higordiegoti/keyrus/internal/bddcatalog"
	"github.com/higordiegoti/keyrus/internal/bddguard"
)

func TestCurrentStepSourcesAreNonVacuous(t *testing.T) {
	if err := bddguard.ValidateStepSources("steps"); err != nil {
		t.Fatal(err)
	}
}

func TestStepSourceGuardFixtures(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantError string
	}{
		{name: "real binding", path: "testdata/guard/real"},
		{name: "pending in test file", path: "testdata/guard/pending", wantError: "ErrPending"},
		{name: "skip in test file", path: "testdata/guard/skip", wantError: "test skip call Skip"},
		{name: "trivial nil handler", path: "testdata/guard/trivial", wantError: "only returns nil"},
		{name: "trivial Given handler", path: "testdata/guard/trivial-given", wantError: "only returns nil"},
		{name: "trivial When handler", path: "testdata/guard/trivial-when", wantError: "only returns nil"},
		{name: "trivial Then handler", path: "testdata/guard/trivial-then", wantError: "only returns nil"},
		{name: "trivial cross-file function", path: "testdata/guard/cross-file", wantError: "only returns nil"},
		{name: "trivial method", path: "testdata/guard/method", wantError: "only returns nil"},
		{name: "real cross-file method", path: "testdata/guard/real-method"},
		{name: "import selector does not collide with local method", path: "testdata/guard/import-collision", wantError: "cannot be resolved within its package"},
		{name: "trivial context identity", path: "testdata/guard/trivial-context", wantError: "no observable effect"},
		{name: "trivial background context", path: "testdata/guard/trivial-background", wantError: "no observable effect"},
		{name: "trivial blank assignment", path: "testdata/guard/trivial-blank-assignment", wantError: "no observable effect"},
		{name: "trivial error conversion", path: "testdata/guard/trivial-error-conversion", wantError: "no observable effect"},
		{name: "real context binding", path: "testdata/guard/real-context"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := bddguard.ValidateStepSources(test.path)
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("got %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestStepSourceGuardRejectsMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	err := bddguard.ValidateStepSources(missing)
	if err == nil {
		t.Fatal("missing guard root passed vacuously")
	}
}

func TestManifestRejectsUnknownScenarioTag(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "implemented.txt")
	if err := os.WriteFile(manifestPath, []byte("@SCN-UNKNOWN-999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := bddcatalog.Load(featuresDir, manifestPath)
	if err == nil || !strings.Contains(err.Error(), "unknown tag") {
		t.Fatalf("got %v, want unknown manifest tag error", err)
	}
}

func TestRuleIsRejectedPreciselyInsteadOfDiscarded(t *testing.T) {
	root := t.TempDir()
	feature := `# language: pt
Funcionalidade: Fixture com regra
  Regra: Uma regra agrupadora
    @SCN-FIXTURE-001
    Cenário: Cenário sob regra
      Dado algo
      Então algo acontece
`
	if err := os.WriteFile(filepath.Join(root, "rule.feature"), []byte(feature), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "implemented.txt")
	if err := os.WriteFile(manifestPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := bddcatalog.Load(root, manifestPath)
	if err == nil || !strings.Contains(err.Error(), "contains Regra:") {
		t.Fatalf("got %v, want precise Regra: rejection", err)
	}
}

func TestCatalogRejectsFeatureWithoutPortugueseLanguageHeader(t *testing.T) {
	root := t.TempDir()
	feature := `Funcionalidade: Fixture sem dialeto explícito
  @SCN-FIXTURE-001
  Cenário: Cenário inválido para o catálogo
    Dado algo
    Então algo acontece
`
	if err := os.WriteFile(filepath.Join(root, "missing-language.feature"), []byte(feature), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "implemented.txt")
	if err := os.WriteFile(manifestPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := bddcatalog.Load(root, manifestPath)
	if err == nil || !strings.Contains(err.Error(), "# language: pt on the first line") {
		t.Fatalf("got %v, want explicit Portuguese language header error", err)
	}
}
