// Package bddcatalog validates the executable Gherkin inventory and its explicit
// implemented-scenario manifest. It contains no business step definitions.
package bddcatalog

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gherkin "github.com/cucumber/gherkin/go/v26"
	messages "github.com/cucumber/messages/go/v21"
)

const expectedScenarioCount = 81

// Catalog is the validated, stable inventory consumed by CI and the Godog harness.
type Catalog struct {
	FeatureFiles         []string `json:"feature_files"`
	ScenarioTags         []string `json:"scenario_tags"`
	ImplementedScenarios []string `json:"implemented_scenarios"`
}

// Load parses every .feature file, enforces one Feature per file and one unique
// @SCN-* tag per declared Scenario/Scenario Outline, then validates the manifest.
func Load(featuresDir, manifestPath string) (Catalog, error) {
	paths, err := featurePaths(featuresDir)
	if err != nil {
		return Catalog{}, err
	}
	if len(paths) == 0 {
		return Catalog{}, fmt.Errorf("no .feature files found in %s", featuresDir)
	}

	seen := make(map[string]string, expectedScenarioCount)
	var tags []string
	for _, path := range paths {
		fileTags, err := parseFeature(path)
		if err != nil {
			return Catalog{}, err
		}
		for _, tag := range fileTags {
			if firstPath, exists := seen[tag]; exists {
				return Catalog{}, fmt.Errorf("duplicate scenario tag %s in %s and %s", tag, firstPath, path)
			}
			seen[tag] = path
			tags = append(tags, tag)
		}
	}
	if len(tags) != expectedScenarioCount {
		return Catalog{}, fmt.Errorf("expected %d unique @SCN-* tags, found %d", expectedScenarioCount, len(tags))
	}
	sort.Strings(tags)

	implemented, err := loadManifest(manifestPath, seen)
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{FeatureFiles: paths, ScenarioTags: tags, ImplementedScenarios: implemented}, nil
}

func featurePaths(root string) ([]string, error) {
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
		return nil, fmt.Errorf("walk features: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

func parseFeature(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	doc, err := gherkin.ParseGherkinDocumentForLanguage(f, "pt", (&messages.Incrementing{}).NewId)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Feature == nil {
		return nil, fmt.Errorf("%s must contain exactly one Funcionalidade", path)
	}

	var tags []string
	for _, child := range doc.Feature.Children {
		if child.Rule != nil {
			return nil, fmt.Errorf("%s contains Regra: %q; Regra: is not supported by the flat scenario catalog", path, child.Rule.Name)
		}
		if child.Scenario == nil {
			continue
		}
		var scenarioTags []string
		for _, tag := range child.Scenario.Tags {
			if strings.HasPrefix(tag.Name, "@SCN-") {
				scenarioTags = append(scenarioTags, tag.Name)
			}
		}
		if len(scenarioTags) != 1 {
			return nil, fmt.Errorf("%s scenario %q must have exactly one @SCN-* tag, found %d", path, child.Scenario.Name, len(scenarioTags))
		}
		tags = append(tags, scenarioTags[0])
	}
	return tags, nil
}

func loadManifest(path string, known map[string]string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()

	seen := make(map[string]struct{})
	var implemented []string
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		value := strings.TrimSpace(scanner.Text())
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		if !strings.HasPrefix(value, "@SCN-") || strings.ContainsAny(value, " \t") {
			return nil, fmt.Errorf("manifest line %d must contain exactly one @SCN-* tag", line)
		}
		if _, exists := known[value]; !exists {
			return nil, fmt.Errorf("manifest line %d references unknown tag %s", line, value)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("manifest line %d duplicates %s", line, value)
		}
		seen[value] = struct{}{}
		implemented = append(implemented, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	sort.Strings(implemented)
	return implemented, nil
}
