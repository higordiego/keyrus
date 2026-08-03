// Package opsec guards the build tooling itself: a runtime security fix is
// worthless if the harness that runs it leaks the secret the fix depends on.
package opsec_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// hexKeyPattern matches anything shaped like the 32-byte hex-encoded
// attestation key runtimeevidence mints (64 hex characters). It is
// deliberately generic -- not the exact key value from any one run -- so it
// also catches a differently-generated key that still leaks the same way.
var hexKeyPattern = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)

// TestMakeDryRunNeverPrintsTheEvidenceAttestationKey reproduces the reviewer's
// first proof: `make -n test`/`make -n reports` require no real execution and
// must not print anything shaped like the attestation key. Before this fixup,
// the key was a Makefile recipe variable substituted directly into an echoed
// command line, so it appeared in this exact dry-run output.
func TestMakeDryRunNeverPrintsTheEvidenceAttestationKey(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, target := range []string{"test", "reports"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			command := exec.Command("make", "-n", target)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("make -n %s: %v\n%s", target, err, output)
			}
			if match := hexKeyPattern.Find(output); match != nil {
				t.Fatalf("make -n %s printed a 32-byte hex value that could be the attestation key: %q\nfull output:\n%s",
					target, match, output)
			}
		})
	}
}

// TestMakefileNeverEmbedsTheKeyInARecipeLine is the structural counterpart to
// the dry-run proof: it fails if the key ever again becomes literal recipe
// text, regardless of whether a particular dry run happens to reveal it. The
// original bug was a Make variable (`$(RUNTIME_EVIDENCE_KEY)`) referenced
// inside a `test`/`reports` recipe line; this asserts the Makefile no longer
// defines any such variable at all, so the key can only ever exist inside a
// file on disk.
func TestMakefileNeverEmbedsTheKeyInARecipeLine(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, forbidden := range []string{"RUNTIME_EVIDENCE_KEY", "CASHFLOW_RUNTIME_EVIDENCE_KEY="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Makefile references %q; the attestation key must never be a Make variable or recipe-line value", forbidden)
		}
	}
}

// TestEvidenceKeyScriptsNeverTraceOrPrintTheKey statically guards the second
// half of the fix: even with the key confined to a private file, a stray
// `set -x` (or an `sh -x` shebang) in the scripts that mint or read it would
// make the shell echo every command with its expanded arguments -- including
// the key -- to stderr. It also fails if a script ever prints the key file's
// contents or path in a way a log could capture.
func TestEvidenceKeyScriptsNeverTraceOrPrintTheKey(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	for _, script := range []string{"scripts/run-tests.sh", "scripts/run-reports.sh"} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			contents, err := os.ReadFile(filepath.Join(root, script))
			if err != nil {
				t.Fatal(err)
			}
			text := string(contents)
			firstLine := strings.SplitN(text, "\n", 2)[0]
			if strings.Contains(text, "set -x") || strings.HasSuffix(strings.TrimSpace(firstLine), "-x") {
				t.Fatalf("%s enables shell command tracing, which would echo the attestation key to stderr", script)
			}
			for _, leak := range []string{
				`cat "$evidence_key_file"`, `cat $evidence_key_file`,
				`echo "$evidence_key_file"`, `echo $evidence_key_file`,
				`echo "$CASHFLOW_RUNTIME_EVIDENCE_KEY_FILE"`,
			} {
				if strings.Contains(text, leak) {
					t.Fatalf("%s contains %q, which could print the key file's path or contents into a log", script, leak)
				}
			}
		})
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
