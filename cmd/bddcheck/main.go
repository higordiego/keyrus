package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/higordiegoti/keyrus/internal/bddcatalog"
)

func main() {
	features := flag.String("features", "features", "directory containing .feature files")
	manifest := flag.String("manifest", "features/implemented_scenarios.txt", "implemented scenario manifest")
	jsonOutput := flag.Bool("json", false, "emit the validated catalog as JSON")
	flag.Parse()

	catalog, err := bddcatalog.Load(*features, *manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(catalog); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("validated %d features, %d unique scenarios, %d implemented\n", len(catalog.FeatureFiles), len(catalog.ScenarioTags), len(catalog.ImplementedScenarios))
}
