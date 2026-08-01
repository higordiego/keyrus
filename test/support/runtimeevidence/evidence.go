// Package runtimeevidence carries machine-readable, scenario-specific results
// from the real T02 container stack into the Godog bindings.
package runtimeevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion  = 2
	SuiteVersion   = "t02-edge-identity-runtime-v2"
	Runtime        = "keycloak+krakend+ledger-image+consolidation-image+otel-collector+fault-backend"
	DefaultCase    = "default"
	maxEvidenceAge = 2 * time.Hour
)

type Oracle struct {
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type Case struct {
	Oracles map[string]Oracle `json:"oracles"`
}

type Scenario struct {
	Cases map[string]Case `json:"cases"`
}

type Evidence struct {
	SchemaVersion int                 `json:"schema_version"`
	SuiteVersion  string              `json:"suite_version"`
	RunID         string              `json:"run_id"`
	Revision      string              `json:"revision"`
	SourceDigest  string              `json:"source_digest_sha256"`
	GeneratedAt   time.Time           `json:"generated_at"`
	ValidUntil    time.Time           `json:"valid_until"`
	Runtime       string              `json:"runtime"`
	Scenarios     map[string]Scenario `json:"scenarios"`
	Integrity     string              `json:"integrity_sha256"`
}

var required = map[string]map[string][]string{
	"@SCN-RNF06-001": {DefaultCase: {"valid_identity", "authorized_operation", "merchant_derived", "tenant_limited"}},
	"@SCN-RNF06-002": {
		"ausente":                 {"condition_exercised", "protected_operation", "rejected", "no_effect_or_disclosure"},
		"expirado":                {"condition_exercised", "protected_operation", "rejected", "no_effect_or_disclosure"},
		"com assinatura inválida": {"condition_exercised", "protected_operation", "rejected", "no_effect_or_disclosure"},
		"sem o escopo exigido":    {"condition_exercised", "protected_operation", "rejected", "no_effect_or_disclosure"},
	},
	"@SCN-RNF06-003": {DefaultCase: {"foreign_resource", "access_attempted", "denied", "existence_hidden", "contract_equal", "timing_indistinguishable"}},
	"@SCN-RNF08-002": {
		"ausente":                 {"condition_exercised", "public_edge_call", "rejected_without_forward"},
		"expirado":                {"condition_exercised", "public_edge_call", "rejected_without_forward"},
		"com assinatura inválida": {"condition_exercised", "public_edge_call", "rejected_without_forward"},
	},
	"@SCN-RNF08-003": {DefaultCase: {"direct_private_call", "invalid_operation_jwt", "service_validated", "rejected", "no_commit"}},
	"@SCN-RNF08-004": {DefaultCase: {"four_headers_sent", "edge_forwarded", "four_headers_preserved"}},
	"@SCN-RNF08-005": {DefaultCase: {"commit_then_eof", "edge_observed_failure", "single_gateway_invocation", "idempotent_client_replay"}},
	"@SCN-RNF08-008": {DefaultCase: {"keycloak_internal", "external_probe", "private_paths_absent", "public_oidc_only"}},
	"@SCN-RNF08-009": {DefaultCase: {"watermark_internal", "missing_service_identity", "ledger_rejected", "no_public_route"}},
	"@SCN-RNF09-004": {DefaultCase: {"context_and_deadline", "crossed_grpc", "traceparent_correlated", "limits_enforced", "telemetry_redacted"}},
}

func New(runID, revision, sourceDigest string, now time.Time) Evidence {
	return Evidence{
		SchemaVersion: SchemaVersion,
		SuiteVersion:  SuiteVersion,
		RunID:         runID,
		Revision:      revision,
		SourceDigest:  sourceDigest,
		GeneratedAt:   now.UTC(),
		ValidUntil:    now.UTC().Add(maxEvidenceAge),
		Runtime:       Runtime,
		Scenarios:     make(map[string]Scenario),
	}
}

// Pass records one exact oracle. Unknown scenario/case/oracle combinations are
// rejected so a generic list or counter cannot accidentally satisfy Godog.
func (e *Evidence) Pass(tag, caseID, oracle, detail string) error {
	if !isRequired(tag, caseID, oracle) {
		return fmt.Errorf("runtimeevidence: unknown oracle %s/%s/%s", tag, caseID, oracle)
	}
	if detail == "" {
		return errors.New("runtimeevidence: oracle detail is required")
	}
	scenario := e.Scenarios[tag]
	if scenario.Cases == nil {
		scenario.Cases = make(map[string]Case)
	}
	testCase := scenario.Cases[caseID]
	if testCase.Oracles == nil {
		testCase.Oracles = make(map[string]Oracle)
	}
	testCase.Oracles[oracle] = Oracle{Passed: true, Detail: detail}
	scenario.Cases[caseID] = testCase
	e.Scenarios[tag] = scenario
	return nil
}

// Require is the literal Godog oracle. Every step names the exact result it
// needs; a result belonging to another example or scenario cannot satisfy it.
func Require(e Evidence, tag, caseID, oracle string) error {
	if !isRequired(tag, caseID, oracle) {
		return fmt.Errorf("runtimeevidence: binding requested unknown oracle %s/%s/%s", tag, caseID, oracle)
	}
	result, ok := e.Scenarios[tag].Cases[caseID].Oracles[oracle]
	if !ok || !result.Passed || result.Detail == "" {
		return fmt.Errorf("runtimeevidence: required oracle failed or is absent: %s/%s/%s", tag, caseID, oracle)
	}
	return nil
}

func Load(path string) (Evidence, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Evidence{}, err
	}
	var evidence Evidence
	if err := json.Unmarshal(contents, &evidence); err != nil {
		return Evidence{}, err
	}
	if err := validate(evidence, time.Now().UTC(), true); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

// LoadForSource additionally binds the evidence to the exact checked-out tree.
// Any source edit makes a prior run unusable and forces a new real E2E.
func LoadForSource(path, repositoryRoot string) (Evidence, error) {
	evidence, err := Load(path)
	if err != nil {
		return Evidence{}, err
	}
	digest, err := SourceDigest(repositoryRoot)
	if err != nil {
		return Evidence{}, err
	}
	if evidence.SourceDigest != digest {
		return Evidence{}, errors.New("runtimeevidence: evidence belongs to a different source tree")
	}
	return evidence, nil
}

func Write(path string, evidence Evidence) error {
	evidence.Integrity = ""
	if err := validate(evidence, evidence.GeneratedAt, false); err != nil {
		return err
	}
	digest, err := checksum(evidence)
	if err != nil {
		return err
	}
	evidence.Integrity = digest
	contents, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(contents, '\n'), 0o600)
}

func validate(e Evidence, now time.Time, verifyIntegrity bool) error {
	if e.SchemaVersion != SchemaVersion || e.SuiteVersion != SuiteVersion || e.Runtime != Runtime {
		return errors.New("runtimeevidence: evidence provenance is invalid")
	}
	if e.RunID == "" || !isRevision(e.Revision) || !isDigest(e.SourceDigest) || e.GeneratedAt.IsZero() || e.ValidUntil.IsZero() {
		return errors.New("runtimeevidence: run identity is invalid")
	}
	if e.GeneratedAt.After(now.Add(time.Minute)) || !e.ValidUntil.After(now) || e.ValidUntil.After(e.GeneratedAt.Add(maxEvidenceAge)) {
		return errors.New("runtimeevidence: evidence is stale or has an invalid validity window")
	}
	if len(e.Scenarios) != len(required) {
		return fmt.Errorf("runtimeevidence: got %d scenarios, want %d", len(e.Scenarios), len(required))
	}
	for tag, cases := range required {
		scenario, ok := e.Scenarios[tag]
		if !ok || len(scenario.Cases) != len(cases) {
			return fmt.Errorf("runtimeevidence: incomplete cases for %s", tag)
		}
		for caseID, oracles := range cases {
			got, ok := scenario.Cases[caseID]
			if !ok || len(got.Oracles) != len(oracles) {
				return fmt.Errorf("runtimeevidence: incomplete oracles for %s/%s", tag, caseID)
			}
			for _, oracle := range oracles {
				if err := Require(e, tag, caseID, oracle); err != nil {
					return err
				}
			}
		}
	}
	if verifyIntegrity {
		provided := e.Integrity
		e.Integrity = ""
		expected, err := checksum(e)
		if err != nil {
			return err
		}
		if provided == "" || provided != expected {
			return errors.New("runtimeevidence: integrity check failed")
		}
	}
	return nil
}

func checksum(e Evidence) (string, error) {
	contents, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

func isRequired(tag, caseID, oracle string) bool {
	for _, expected := range required[tag][caseID] {
		if expected == oracle {
			return true
		}
	}
	return false
}

func isRevision(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// SourceDigest hashes every tracked or untracked non-ignored file. Paths are
// sorted and included in the digest, preventing both content and file-name
// substitutions from reusing evidence produced by another source tree.
func SourceDigest(repositoryRoot string) (string, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return "", err
	}
	command := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("runtimeevidence: enumerate source tree: %w", err)
	}
	paths := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		if path == "" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return "", fmt.Errorf("runtimeevidence: read source %s: %w", path, err)
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(contents)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// RequiredKeys is exposed only for focused structure tests and diagnostics.
func RequiredKeys() []string {
	keys := make([]string, 0, len(required))
	for key := range required {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
