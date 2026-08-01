package runtimeevidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testRevision = "9203ffa17ab74ff501e16bfbbcdac74dfa238a91"
const testSourceDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestEvidenceRoundTripRequiresEveryLiteralOracle(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	evidence := completeEvidence(t, now)
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := Write(path, evidence); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Require(loaded, "@SCN-RNF06-002", "expirado", "rejected"); err != nil {
		t.Fatal(err)
	}

	delete(evidence.Scenarios["@SCN-RNF08-005"].Cases[DefaultCase].Oracles, "commit_then_eof")
	if err := Write(filepath.Join(t.TempDir(), "incomplete.json"), evidence); err == nil || !strings.Contains(err.Error(), "incomplete oracles") {
		t.Fatalf("incomplete evidence was accepted: %v", err)
	}
}

func TestEvidenceRejectsTamperingAndStaleness(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := Write(path, completeEvidence(t, now)); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(contents, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["run_id"] = "tampered-run"
	tampered, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tampered evidence was accepted: %v", err)
	}

	stale := completeEvidence(t, now.Add(-3*time.Hour))
	stalePath := filepath.Join(t.TempDir(), "stale.json")
	if err := Write(stalePath, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(stalePath); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale evidence was accepted: %v", err)
	}
}

func completeEvidence(t *testing.T, now time.Time) Evidence {
	t.Helper()
	evidence := New("unit-test-run", testRevision, testSourceDigest, now)
	for tag, cases := range required {
		for caseID, oracles := range cases {
			for _, oracle := range oracles {
				if err := evidence.Pass(tag, caseID, oracle, "literal unit-test detail"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return evidence
}
