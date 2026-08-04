package bddsource

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Digests is the notarized fingerprint of the authored Gherkin, keyed by
// Funcionalidade name and by @SCN-* tag. It is committed to the repository so CI
// can prove the .feature files still carry the text that was blessed against the
// planning artifacts.
type Digests struct {
	Note      string            `json:"note"`
	Features  map[string]string `json:"features"`
	Scenarios map[string]string `json:"scenarios"`
}

const digestNote = "Regenerate with: make bdd-bless BDD_SOURCE=<path to bdd-requirements>. Never edit by hand."

// ComputeDigests reduces parsed features to their digest fingerprint.
func ComputeDigests(features []Feature) Digests {
	computed := Digests{
		Note:      digestNote,
		Features:  make(map[string]string, len(features)),
		Scenarios: make(map[string]string),
	}
	for _, feature := range features {
		computed.Features[feature.Name] = digest(feature.Header)
		for _, scenario := range feature.Scenarios {
			computed.Scenarios[scenario.Tag] = digest(scenario.Lines)
		}
	}
	return computed
}

// LoadDigests reads a committed digest file.
func LoadDigests(path string) (Digests, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Digests{}, fmt.Errorf("read digests %s: %w", path, err)
	}
	var loaded Digests
	if err := json.Unmarshal(contents, &loaded); err != nil {
		return Digests{}, fmt.Errorf("decode digests %s: %w", path, err)
	}
	return loaded, nil
}

// SaveDigests writes a digest file deterministically, so regenerating it without a
// real change produces no diff.
func SaveDigests(path string, computed Digests) error {
	encoded, err := json.MarshalIndent(computed, "", "  ")
	if err != nil {
		return fmt.Errorf("encode digests: %w", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("write digests %s: %w", path, err)
	}
	return nil
}

// CompareDigests reports every difference between the committed fingerprint and the
// one recomputed from the .feature files.
func CompareDigests(committed, recomputed Digests) []string {
	var differences []string
	differences = append(differences, compareMaps("Funcionalidade", committed.Features, recomputed.Features)...)
	differences = append(differences, compareMaps("scenario", committed.Scenarios, recomputed.Scenarios)...)
	return differences
}

func compareMaps(kind string, committed, recomputed map[string]string) []string {
	var differences []string
	for _, key := range sortedKeys(committed) {
		current, exists := recomputed[key]
		if !exists {
			differences = append(differences, fmt.Sprintf("%s %s is in the digest file but no longer in the .feature files", kind, key))
			continue
		}
		if current != committed[key] {
			differences = append(differences, fmt.Sprintf("%s %s changed in the .feature files without a matching artifact bless", kind, key))
		}
	}
	for _, key := range sortedKeys(recomputed) {
		if _, exists := committed[key]; !exists {
			differences = append(differences, fmt.Sprintf("%s %s is in the .feature files but absent from the digest file", kind, key))
		}
	}
	return differences
}

// CompareFeatures reports every difference between the artifacts and the .feature
// files, matching features by their Funcionalidade name.
func CompareFeatures(artifacts, features []Feature) []string {
	authored := indexByName(artifacts)
	executable := indexByName(features)

	var differences []string
	for _, name := range sortedKeys(authored) {
		executableFeature, exists := executable[name]
		if !exists {
			differences = append(differences, fmt.Sprintf("Funcionalidade %q exists in %s but has no .feature file", name, authored[name].Origin))
			continue
		}
		differences = append(differences, compareFeature(authored[name], executableFeature)...)
	}
	for _, name := range sortedKeys(executable) {
		if _, exists := authored[name]; !exists {
			differences = append(differences, fmt.Sprintf("Funcionalidade %q exists in %s but is absent from the artifacts", name, executable[name].Origin))
		}
	}
	return differences
}

func compareFeature(authored, executable Feature) []string {
	var differences []string
	if difference := compareLines(authored.Header, executable.Header); difference != "" {
		differences = append(differences, fmt.Sprintf("Funcionalidade %q header differs (%s vs %s): %s",
			authored.Name, authored.Origin, executable.Origin, difference))
	}

	authoredScenarios := indexByTag(authored)
	executableScenarios := indexByTag(executable)
	for _, tag := range sortedKeys(authoredScenarios) {
		executableScenario, exists := executableScenarios[tag]
		if !exists {
			differences = append(differences, fmt.Sprintf("%s is in %s but missing from %s", tag, authored.Origin, executable.Origin))
			continue
		}
		if difference := compareLines(authoredScenarios[tag].Lines, executableScenario.Lines); difference != "" {
			differences = append(differences, fmt.Sprintf("%s differs: %s", tag, difference))
		}
	}
	for _, tag := range sortedKeys(executableScenarios) {
		if _, exists := authoredScenarios[tag]; !exists {
			differences = append(differences, fmt.Sprintf("%s is in %s but absent from the artifacts", tag, executable.Origin))
		}
	}
	return differences
}

func compareLines(authored, executable []string) string {
	limit := len(authored)
	if len(executable) < limit {
		limit = len(executable)
	}
	for index := 0; index < limit; index++ {
		if authored[index] != executable[index] {
			return fmt.Sprintf("line %d\n      artifact: %s\n      feature : %s", index+1, authored[index], executable[index])
		}
	}
	if len(authored) != len(executable) {
		return fmt.Sprintf("line count %d in the artifact against %d in the .feature file", len(authored), len(executable))
	}
	return ""
}

func indexByName(features []Feature) map[string]Feature {
	indexed := make(map[string]Feature, len(features))
	for _, feature := range features {
		indexed[feature.Name] = feature
	}
	return indexed
}

func indexByTag(feature Feature) map[string]Scenario {
	indexed := make(map[string]Scenario, len(feature.Scenarios))
	for _, scenario := range feature.Scenarios {
		indexed[scenario.Tag] = scenario
	}
	return indexed
}

func sortedKeys[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// FormatDifferences renders differences as an indented, stable report.
func FormatDifferences(differences []string) string {
	var builder strings.Builder
	for _, difference := range differences {
		builder.WriteString("  - ")
		builder.WriteString(difference)
		builder.WriteString("\n")
	}
	return builder.String()
}
