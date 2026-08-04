// Package bddsource compares the Gherkin authored in the planning artifacts with
// the executable .feature files and notarizes that agreement as per-scenario
// digests.
//
// The planning artifacts live outside the repository, so CI cannot reach them.
// The committed digest file closes that gap: it is regenerated only while the
// artifacts are available, and CI recomputes the same digests from the .feature
// files alone. A .feature edited without a matching artifact edit therefore fails
// CI even though CI never reads the artifacts.
package bddsource

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	fenceOpen        = "```gherkin"
	fenceClose       = "```"
	featureKeyword   = "Funcionalidade:"
	scenarioTagMark  = "@SCN-"
	digestLineJoiner = "\n"
)

// Scenario is one tagged Gherkin scenario reduced to the text that carries meaning.
// Lines are trimmed and blank lines removed so that indentation and spacing differ
// freely between a fenced Markdown block and a standalone .feature file.
type Scenario struct {
	Tag   string
	Lines []string
}

// Feature is a single Funcionalidade with the scenarios declared under it.
// Header holds everything before the first @SCN-* tag: the feature tags, the
// Funcionalidade line, the narrative and any Contexto. Contexto applies to every
// scenario in the file, so it is digested alongside them.
type Feature struct {
	Name      string
	Origin    string
	Header    []string
	Scenarios []Scenario
}

// ParseArtifacts reads every Markdown file under root and returns one Feature per
// fenced ```gherkin block.
func ParseArtifacts(root string) ([]Feature, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && filepath.Ext(path) == ".md" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk artifacts %s: %w", root, err)
	}
	sort.Strings(paths)

	var features []Feature
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		for index, block := range fencedGherkinBlocks(string(contents)) {
			feature, parseErr := parseBlock(block, fmt.Sprintf("%s block %d", path, index+1))
			if parseErr != nil {
				return nil, parseErr
			}
			features = append(features, feature)
		}
	}
	if len(features) == 0 {
		return nil, fmt.Errorf("no ```gherkin blocks found under %s", root)
	}
	return features, dedupe(features)
}

// ParseFeatureFiles reads every .feature file under root and returns one Feature each.
func ParseFeatureFiles(root string) ([]Feature, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && filepath.Ext(path) == ".feature" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk features %s: %w", root, err)
	}
	sort.Strings(paths)

	var features []Feature
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		feature, parseErr := parseBlock(strings.Split(string(contents), "\n"), path)
		if parseErr != nil {
			return nil, parseErr
		}
		features = append(features, feature)
	}
	if len(features) == 0 {
		return nil, fmt.Errorf("no .feature files found under %s", root)
	}
	return features, dedupe(features)
}

func fencedGherkinBlocks(contents string) [][]string {
	var blocks [][]string
	var current []string
	inside := false
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inside {
			if trimmed == fenceOpen {
				inside = true
				current = nil
			}
			continue
		}
		if trimmed == fenceClose {
			inside = false
			blocks = append(blocks, current)
			continue
		}
		current = append(current, line)
	}
	return blocks
}

func parseBlock(lines []string, origin string) (Feature, error) {
	feature := Feature{Origin: origin}
	var currentTag string
	var currentLines []string

	flush := func() {
		if currentTag == "" {
			return
		}
		feature.Scenarios = append(feature.Scenarios, Scenario{Tag: currentTag, Lines: currentLines})
		currentTag = ""
		currentLines = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if tag, ok := scenarioTag(trimmed); ok {
			flush()
			currentTag = tag
			currentLines = []string{trimmed}
			continue
		}
		if currentTag != "" {
			currentLines = append(currentLines, trimmed)
			continue
		}
		if strings.HasPrefix(trimmed, featureKeyword) {
			feature.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, featureKeyword))
		}
		feature.Header = append(feature.Header, trimmed)
	}
	flush()

	if feature.Name == "" {
		return Feature{}, fmt.Errorf("%s declares no %s line", origin, featureKeyword)
	}
	if len(feature.Scenarios) == 0 {
		return Feature{}, fmt.Errorf("%s declares no %s* tagged scenario", origin, scenarioTagMark)
	}
	return feature, nil
}

// scenarioTag reports whether a line is a tag line carrying exactly one @SCN-* tag
// and returns that tag. Companion tags on the same line, such as @evidencia-k6, are
// allowed and stay part of the digested text.
func scenarioTag(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, "@") {
		return "", false
	}
	found := ""
	for _, field := range strings.Fields(trimmed) {
		if !strings.HasPrefix(field, scenarioTagMark) {
			continue
		}
		if found != "" {
			return "", false
		}
		found = field
	}
	return found, found != ""
}

func dedupe(features []Feature) error {
	seen := make(map[string]string, len(features))
	for _, feature := range features {
		if origin, exists := seen[feature.Name]; exists {
			return fmt.Errorf("duplicate Funcionalidade %q in %s and %s", feature.Name, origin, feature.Origin)
		}
		seen[feature.Name] = feature.Origin
	}
	return nil
}

func digest(lines []string) string {
	sum := sha256.Sum256([]byte(strings.Join(lines, digestLineJoiner)))
	return hex.EncodeToString(sum[:])
}
