package bddsource_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higordiegoti/keyrus/internal/bddsource"
)

const artifactMarkdown = "# RF-01\n" +
	"\n" +
	"```gherkin\n" +
	"# language: pt\n" +
	"@RF-01 @critico\n" +
	"Funcionalidade: Registrar lançamentos\n" +
	"  Contexto:\n" +
	"    Dado que o comerciante está autenticado\n" +
	"\n" +
	"  @SCN-RF01-001\n" +
	"  Cenário: Confirmar um lançamento\n" +
	"    Quando o comerciante solicitar o registro\n" +
	"    Então o lançamento deve ser confirmado\n" +
	"\n" +
	"  @SCN-RF01-002 @evidencia-k6\n" +
	"  Cenário: Rejeitar valor inválido\n" +
	"    Quando o valor for zero\n" +
	"    Então nenhum lançamento deve ser confirmado\n" +
	"```\n"

const featureFile = "# language: pt\n" +
	"@RF-01 @critico\n" +
	"Funcionalidade: Registrar lançamentos\n" +
	"  Contexto:\n" +
	"    Dado que o comerciante está autenticado\n" +
	"\n" +
	"  @SCN-RF01-001\n" +
	"  Cenário: Confirmar um lançamento\n" +
	"    Quando o comerciante solicitar o registro\n" +
	"    Então o lançamento deve ser confirmado\n" +
	"\n" +
	"  @SCN-RF01-002 @evidencia-k6\n" +
	"  Cenário: Rejeitar valor inválido\n" +
	"    Quando o valor for zero\n" +
	"    Então nenhum lançamento deve ser confirmado\n"

type tree struct {
	artifactDir string
	featureDir  string
}

func writeTree(t *testing.T, artifact, feature string) tree {
	t.Helper()
	root := t.TempDir()
	built := tree{
		artifactDir: filepath.Join(root, "artifacts"),
		featureDir:  filepath.Join(root, "features"),
	}
	artifactFileDir := filepath.Join(built.artifactDir, "lancamentos")
	for _, dir := range []string{artifactFileDir, built.featureDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(artifactFileDir, "index.md"), []byte(artifact), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(built.featureDir, "registrar.feature"), []byte(feature), 0o644); err != nil {
		t.Fatal(err)
	}
	return built
}

func (built tree) parse(t *testing.T) ([]bddsource.Feature, []bddsource.Feature) {
	t.Helper()
	authored, err := bddsource.ParseArtifacts(built.artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := bddsource.ParseFeatureFiles(built.featureDir)
	if err != nil {
		t.Fatal(err)
	}
	return authored, executable
}

// driftOf writes a .feature that diverges from the artifact and returns the report.
func driftOf(t *testing.T, feature string) []string {
	t.Helper()
	authored, executable := writeTree(t, artifactMarkdown, feature).parse(t)
	return bddsource.CompareFeatures(authored, executable)
}

func TestArtifactAndFeatureAgree(t *testing.T) {
	authored, executable := writeTree(t, artifactMarkdown, featureFile).parse(t)

	if got, want := len(authored[0].Scenarios), 2; got != want {
		t.Fatalf("scenario count: got %d, want %d", got, want)
	}
	if got, want := authored[0].Name, "Registrar lançamentos"; got != want {
		t.Fatalf("feature name: got %q, want %q", got, want)
	}
	if differences := bddsource.CompareFeatures(authored, executable); len(differences) != 0 {
		t.Fatalf("expected no drift, got:\n%s", bddsource.FormatDifferences(differences))
	}
}

// Indentation and blank lines are presentation, not requirement text: a fenced
// Markdown block and a .feature file are allowed to differ there.
func TestIndentationAndBlankLinesAreNotDrift(t *testing.T) {
	reindented := strings.ReplaceAll(featureFile, "    Então", "        Então")
	reindented = strings.ReplaceAll(reindented, "  @SCN-", "\n\n@SCN-")

	if differences := driftOf(t, reindented); len(differences) != 0 {
		t.Fatalf("reformatting must not count as drift, got:\n%s", bddsource.FormatDifferences(differences))
	}
}

func TestDetectsChangedStepText(t *testing.T) {
	differences := driftOf(t, strings.Replace(featureFile,
		"Então o lançamento deve ser confirmado",
		"Então o lançamento pode ser confirmado", 1))

	if len(differences) == 0 {
		t.Fatal("changed step text was not reported as drift")
	}
	if !strings.Contains(differences[0], "@SCN-RF01-001") {
		t.Fatalf("drift should name the scenario tag, got: %s", differences[0])
	}
}

func TestDetectsDroppedCompanionTag(t *testing.T) {
	differences := driftOf(t, strings.Replace(featureFile,
		"@SCN-RF01-002 @evidencia-k6", "@SCN-RF01-002", 1))

	if len(differences) == 0 {
		t.Fatal("dropping @evidencia-k6 was not reported as drift")
	}
}

func TestDetectsChangedContexto(t *testing.T) {
	differences := driftOf(t, strings.Replace(featureFile,
		"Dado que o comerciante está autenticado",
		"Dado que o comerciante é anônimo", 1))

	if len(differences) == 0 {
		t.Fatal("changed Contexto was not reported as drift")
	}
	if !strings.Contains(differences[0], "header differs") {
		t.Fatalf("Contexto drift should be reported against the feature header, got: %s", differences[0])
	}
}

func TestDetectsMissingScenario(t *testing.T) {
	differences := driftOf(t, strings.Split(featureFile, "  @SCN-RF01-002")[0])

	if len(differences) == 0 {
		t.Fatal("a scenario missing from the .feature file was not reported")
	}
	if !strings.Contains(differences[0], "@SCN-RF01-002") {
		t.Fatalf("drift should name the missing tag, got: %s", differences[0])
	}
}

func TestDigestsDetectFeatureEditedWithoutBless(t *testing.T) {
	_, blessed := writeTree(t, artifactMarkdown, featureFile).parse(t)
	committed := bddsource.ComputeDigests(blessed)

	drifted := writeTree(t, artifactMarkdown, strings.Replace(featureFile,
		"Então o lançamento deve ser confirmado",
		"Então o lançamento deve ser ignorado", 1))
	recomputed, err := bddsource.ParseFeatureFiles(drifted.featureDir)
	if err != nil {
		t.Fatal(err)
	}

	differences := bddsource.CompareDigests(committed, bddsource.ComputeDigests(recomputed))
	if len(differences) == 0 {
		t.Fatal("editing a .feature without re-blessing was not caught by the digests")
	}
	if !strings.Contains(differences[0], "@SCN-RF01-001") {
		t.Fatalf("digest drift should name the scenario tag, got: %s", differences[0])
	}
}

func TestDigestsRoundTrip(t *testing.T) {
	_, executable := writeTree(t, artifactMarkdown, featureFile).parse(t)
	computed := bddsource.ComputeDigests(executable)

	path := filepath.Join(t.TempDir(), "source_digests.json")
	if err := bddsource.SaveDigests(path, computed); err != nil {
		t.Fatal(err)
	}
	loaded, err := bddsource.LoadDigests(path)
	if err != nil {
		t.Fatal(err)
	}
	if differences := bddsource.CompareDigests(loaded, computed); len(differences) != 0 {
		t.Fatalf("digests did not survive a round trip:\n%s", bddsource.FormatDifferences(differences))
	}
}

func TestRejectsBlockWithoutFuncionalidade(t *testing.T) {
	broken := "```gherkin\n# language: pt\n@SCN-RF01-001\nCenário: sem funcionalidade\n  Dado algo\n```\n"
	built := writeTree(t, broken, featureFile)
	if _, err := bddsource.ParseArtifacts(built.artifactDir); err == nil {
		t.Fatal("a gherkin block without Funcionalidade should be rejected")
	}
}

func TestRejectsDuplicateFuncionalidade(t *testing.T) {
	built := writeTree(t, artifactMarkdown+"\n"+artifactMarkdown, featureFile)
	if _, err := bddsource.ParseArtifacts(built.artifactDir); err == nil {
		t.Fatal("two blocks sharing a Funcionalidade name should be rejected")
	}
}
