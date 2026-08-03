package reporting_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReportGenerationRunsEveryProducerAndReturnsTheFirstFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the report generator is a POSIX shell script")
	}

	tests := []struct {
		name       string
		testStatus string
		bddStatus  string
		wantStatus int
	}{
		{name: "go tests fail", testStatus: "7", bddStatus: "0", wantStatus: 7},
		{name: "catalog fails", testStatus: "0", bddStatus: "9", wantStatus: 9},
		{name: "both fail", testStatus: "7", bddStatus: "9", wantStatus: 7},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			reportsDir := filepath.Join(root, "reports")
			if err := os.MkdirAll(binDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(reportsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"go-test.json", "bdd-catalog.json"} {
				if err := os.WriteFile(filepath.Join(reportsDir, name), []byte("stale\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			fakeGo := filepath.Join(binDir, "go")
			contents := `#!/bin/sh
set -eu
printf '%s\n' "$1" >>"$REPORT_CALLS"
case "$1" in
test)
	printf '%s\n' current-go-test
	exit "$REPORT_TEST_STATUS"
	;;
run)
	printf '%s\n' current-bdd-catalog
	exit "$REPORT_BDD_STATUS"
	;;
*)
	exit 99
	;;
esac
`
			if err := os.WriteFile(fakeGo, []byte(contents), 0o700); err != nil {
				t.Fatal(err)
			}

			callsPath := filepath.Join(root, "calls")
			command := exec.Command("../../scripts/generate-reports.sh", reportsDir)
			command.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"REPORT_CALLS="+callsPath,
				"REPORT_TEST_STATUS="+test.testStatus,
				"REPORT_BDD_STATUS="+test.bddStatus,
			)
			err := command.Run()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != test.wantStatus {
				t.Fatalf("got error %v, want exit status %d", err, test.wantStatus)
			}

			assertContents(t, callsPath, "test\nrun\n")
			assertContents(t, filepath.Join(reportsDir, "go-test.json"), "current-go-test\n")
			assertContents(t, filepath.Join(reportsDir, "bdd-catalog.json"), "current-bdd-catalog\n")
		})
	}
}

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(contents); got != want {
		t.Fatalf("%s: got %q, want %q", path, got, want)
	}
	if strings.Contains(string(contents), "stale") {
		t.Fatalf("%s retained stale report content", path)
	}
}
