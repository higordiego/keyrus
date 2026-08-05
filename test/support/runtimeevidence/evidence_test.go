package runtimeevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testRevision = "8d01f0b018096a0553b310af469f7de71821d2f4"
const testSourceDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestEvidenceRoundTripRequiresEveryLiteralOracle(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	now := time.Now().UTC()
	evidence := completeEvidence(t, now)
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := Write(path, evidence, key); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := RequireValue(loaded, "@SCN-RNF06-002", "expirado", "rejected", "edge_status", "401"); err != nil {
		t.Fatal(err)
	}

	delete(evidence.Scenarios["@SCN-RNF08-005"].Cases[DefaultCase].Oracles, "commit_then_eof")
	if err := Write(filepath.Join(t.TempDir(), "incomplete.json"), evidence, key); err == nil ||
		!strings.Contains(err.Error(), "incomplete oracles") {
		t.Fatalf("incomplete evidence was accepted: %v", err)
	}
}

// This is the reviewer's exact bypass: rewrite the oracle values, recompute the
// public checksum with the public algorithm, and try to make the gate pass
// without a runtime. The keyed attestation must reject it.
func TestForgedOraclesWithRecomputedPublicChecksumAreRejected(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := Write(path, completeEvidence(t, time.Now().UTC()), key); err != nil {
		t.Fatal(err)
	}
	forged := forgeEveryOracle(t, path)

	if _, err := Load(forged, key); err == nil || !strings.Contains(err.Error(), "attestation") {
		t.Fatalf("forged evidence with a recomputed public checksum was accepted: %v", err)
	}

	if _, err := Load(forged, testKey(t)); err == nil {
		t.Fatal("forged evidence was accepted under an attacker-chosen key")
	}
}

func TestTamperedSingleObservationIsRejected(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := Write(path, completeEvidence(t, time.Now().UTC()), key); err != nil {
		t.Fatal(err)
	}
	decoded := decode(t, path)
	scenarios := decoded["scenarios"].(map[string]any)
	oracle := scenarios["@SCN-RNF08-002"].(map[string]any)["cases"].(map[string]any)["ausente"].(map[string]any)["oracles"].(map[string]any)["rejected_without_forward"].(map[string]any)
	oracle["observations"].(map[string]any)["entrypoint_after"] = "99"
	tampered := reseal(t, filepath.Join(t.TempDir(), "tampered.json"), decoded)

	if _, err := Load(tampered, key); err == nil || !strings.Contains(err.Error(), "attestation") {
		t.Fatalf("a single tampered observation with a recomputed checksum was accepted: %v", err)
	}
}

func TestEvidenceRejectsCorruptionStalenessAndEmptyObservations(t *testing.T) {
	t.Parallel()
	key := testKey(t)
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := Write(path, completeEvidence(t, now), key); err != nil {
		t.Fatal(err)
	}
	decoded := decode(t, path)
	decoded["run_id"] = "tampered-run"
	tampered, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := filepath.Join(t.TempDir(), "corrupted.json")
	if err := os.WriteFile(corrupted, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(corrupted, key); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("corrupted evidence was accepted: %v", err)
	}

	stalePath := filepath.Join(t.TempDir(), "stale.json")
	if err := Write(stalePath, completeEvidence(t, now.Add(-3*time.Hour)), key); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(stalePath, key); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale evidence was accepted: %v", err)
	}

	blank := New("unit-test-run", testRevision, testSourceDigest, now)
	if err := blank.Observe("@SCN-RNF06-003", DefaultCase, "denied", "detail", map[string]string{"status": "  "}); err == nil {
		t.Fatal("an empty observation value was accepted")
	}
	if err := blank.Observe("@SCN-RNF06-003", DefaultCase, "denied", "detail", map[string]string{"code": "404"}); err == nil {
		t.Fatal("an unknown observation key was accepted")
	}
}

func TestRequireValueRejectsADifferentObservedValue(t *testing.T) {
	t.Parallel()
	evidence := completeEvidence(t, time.Now().UTC())
	if err := RequireValue(evidence, "@SCN-RNF06-002", "expirado", "rejected", "edge_status", "200"); err == nil {
		t.Fatal("a binding accepted an observed value it did not require")
	}
}

// forgeEveryOracle reproduces the review's attack end to end: every observation
// becomes a fabricated constant and the public checksum is recomputed.
func forgeEveryOracle(t *testing.T, path string) string {
	t.Helper()
	decoded := decode(t, path)
	for _, scenario := range decoded["scenarios"].(map[string]any) {
		for _, testCase := range scenario.(map[string]any)["cases"].(map[string]any) {
			for _, oracle := range testCase.(map[string]any)["oracles"].(map[string]any) {
				fields := oracle.(map[string]any)
				fields["detail"] = "fabricated-without-runtime-observation"
				for key := range fields["observations"].(map[string]any) {
					fields["observations"].(map[string]any)[key] = "fabricated-without-runtime-observation"
				}
			}
		}
	}
	return reseal(t, filepath.Join(t.TempDir(), "forged.json"), decoded)
}

// reseal recomputes the public SHA-256 exactly the way the writer does, which is
// all a forger can do without the run key.
func reseal(t *testing.T, path string, decoded map[string]any) string {
	t.Helper()
	delete(decoded, "integrity_sha256")
	delete(decoded, "attestation_hmac_sha256")
	canonical, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	var normalized Evidence
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		t.Fatal(err)
	}
	canonical, err = json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	decoded["integrity_sha256"] = hex.EncodeToString(digest[:])
	decoded["attestation_hmac_sha256"] = hex.EncodeToString(digest[:])
	contents, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func decode(t *testing.T, path string) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(contents, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func testKey(t *testing.T) []byte {
	t.Helper()
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func completeEvidence(t *testing.T, now time.Time) Evidence {
	t.Helper()
	evidence := New("unit-test-run", testRevision, testSourceDigest, now)
	for tag, cases := range required {
		for caseID, oracles := range cases {
			for oracle, keys := range oracles {
				observations := make(map[string]string, len(keys))
				for _, key := range keys {
					observations[key] = "unit-test-" + key
					if key == "edge_status" {
						observations[key] = "401"
					}
				}
				if err := evidence.Observe(tag, caseID, oracle, "literal unit-test detail", observations); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return evidence
}
