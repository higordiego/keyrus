package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

type report struct {
	Results []result `json:"Results"`
}

type result struct {
	Target          string          `json:"Target"`
	Vulnerabilities []vulnerability `json:"Vulnerabilities"`
}

type vulnerability struct {
	ID           string `json:"VulnerabilityID"`
	Package      string `json:"PkgName"`
	Severity     string `json:"Severity"`
	FixedVersion string `json:"FixedVersion"`
}

func main() {
	reportPath := flag.String("trivy-report", "", "path to a Trivy JSON report")
	requireFix := flag.Bool("require-fix", true, "block only vulnerabilities with a fixed version")
	flag.Parse()
	if *reportPath == "" {
		fmt.Fprintln(os.Stderr, "-trivy-report is required")
		os.Exit(2)
	}

	data, err := os.ReadFile(*reportPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read Trivy report: %v\n", err)
		os.Exit(2)
	}
	var scan report
	if err := json.Unmarshal(data, &scan); err != nil {
		fmt.Fprintf(os.Stderr, "parse Trivy report: %v\n", err)
		os.Exit(2)
	}

	blocked := 0
	for _, result := range scan.Results {
		for _, finding := range result.Vulnerabilities {
			severity := strings.ToUpper(finding.Severity)
			if severity != "HIGH" && severity != "CRITICAL" {
				continue
			}
			if *requireFix && finding.FixedVersion == "" {
				continue
			}
			blocked++
			fmt.Fprintf(os.Stderr, "blocking vulnerability %s severity %s package %s target %s fixed in %s\n",
				finding.ID, severity, finding.Package, result.Target, finding.FixedVersion)
		}
	}
	if blocked > 0 {
		os.Exit(1)
	}
	fmt.Println("no blocking HIGH/CRITICAL vulnerability with an available fix")
}
