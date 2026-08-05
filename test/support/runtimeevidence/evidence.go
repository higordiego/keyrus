// Package runtimeevidence carries machine-readable, scenario-specific results
// from the real T02 container stack into the Godog bindings.
//
// Two properties make the artifact usable as acceptance proof.
//
// First, an oracle is not a claim: it is a set of named observations whose
// values were measured against the running stack. The Godog bindings assert on
// those values, so replacing them with prose or with a placeholder makes the
// binding fail instead of passing.
//
// Second, integrity is attested with a key the file does not carry. The
// verifying process mints an ephemeral key and hands it to the real E2E it
// spawns only through the path to a private (0600) temporary file, never as
// a command-line argument or an echoed variable value, so it cannot end up in
// a shell trace or a build log. A public SHA-256 is still recorded so
// accidental corruption is reported precisely, but recomputing it cannot make a
// hand-written file acceptable: without the run key the attestation fails.
package runtimeevidence

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	SchemaVersion = 3
	SuiteVersion  = "t02-edge-identity-runtime-v3"
	Runtime       = "keycloak+krakend+bypass-krakend+ledger-image+consolidation-image+otel-collector+fault-backend"
	DefaultCase   = "default"

	// KeyFileEnvVar names the private file carrying the ephemeral attestation
	// key handed to the spawned E2E. Only this path travels as an environment
	// value or command-line argument; the key bytes never do, so no build log,
	// shell trace or `make -n` dry run can print them.
	KeyFileEnvVar = "CASHFLOW_RUNTIME_EVIDENCE_KEY_FILE"
	// FileEnvVar names the path the spawned E2E must write.
	FileEnvVar = "CASHFLOW_RUNTIME_EVIDENCE_FILE"

	keyBytes       = 32
	maxEvidenceAge = 2 * time.Hour
)

// Oracle is one named observation set measured against the running stack.
type Oracle struct {
	Detail       string            `json:"detail"`
	Observations map[string]string `json:"observations"`
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
	Attestation   string              `json:"attestation_hmac_sha256"`
}

// required declares, for every scenario tag and Gherkin example, the exact
// oracles the run must produce and the exact observation keys each one must
// carry. A missing key, an empty value or an unknown name is rejected at write
// time and again at load time.
var required = map[string]map[string]map[string][]string{
	"@SCN-RNF06-001": {DefaultCase: {
		"valid_identity":       {"issuer", "audience", "merchant_id", "scope"},
		"authorized_operation": {"edge_status", "path"},
		"merchant_derived":     {"merchant_id", "entry_id"},
		"tenant_limited":       {"cross_status", "own_status"},
	}},
	"@SCN-RNF06-002": {
		"ausente":                 invalidIdentityOracles,
		"expirado":                invalidIdentityOracles,
		"com assinatura inválida": invalidIdentityOracles,
		"sem o escopo exigido":    invalidIdentityOracles,
	},
	"@SCN-RNF06-003": {DefaultCase: {
		"foreign_resource":         {"entry_id", "owner_merchant_id"},
		"access_attempted":         {"caller_merchant_id", "path"},
		"denied":                   {"status"},
		"existence_hidden":         {"body_sha256", "identifiers_disclosed"},
		"contract_equal":           {"cross_status", "absent_status", "body_sha256"},
		"timing_indistinguishable": {"samples", "foreign_median", "absent_median", "difference", "tolerance", "separability"},
	}},
	"@SCN-RNF08-002": {
		"ausente":                 edgeForwardingOracles,
		"expirado":                edgeForwardingOracles,
		"com assinatura inválida": edgeForwardingOracles,
	},
	"@SCN-RNF08-003": {DefaultCase: {
		"direct_private_call":   {"target", "bypassed"},
		"invalid_operation_jwt": {"credential_state"},
		"service_validated":     {"entrypoint_delta"},
		"rejected":              {"status"},
		"no_commit":             {"authenticated_delta"},
	}},
	"@SCN-RNF08-004": {DefaultCase: {
		"four_headers_sent":      {"authorization_sha256", "idempotency_key", "traceparent", "tracestate"},
		"edge_forwarded":         {"backend_invocations", "edge_image"},
		"four_headers_preserved": {"authorization_sha256", "idempotency_key", "trace_id", "tracestate"},
	}},
	"@SCN-RNF08-005": {DefaultCase: {
		"commit_then_eof":           {"invocations", "commits", "replays"},
		"edge_observed_failure":     {"client_transport_error", "gateway_status"},
		"single_gateway_invocation": {"invocations", "commits"},
		"idempotent_client_replay":  {"invocations", "commits", "replays", "replay_status"},
	}},
	"@SCN-RNF08-008": {DefaultCase: {
		"keycloak_internal":    {"published_edge_ports", "keycloak_network_alias"},
		"external_probe":       {"health_probe_path", "health_probe_status", "container_health"},
		"private_paths_absent": {"paths", "statuses"},
		"public_oidc_only":     {"authorization_code_flows", "public_oidc_paths"},
	}},
	"@SCN-RNF08-009": {DefaultCase: {
		"watermark_internal":       {"transport", "target"},
		"missing_service_identity": {"authorization_metadata"},
		"ledger_rejected":          {"grpc_code"},
		"no_public_route":          {"config_endpoints", "probe_statuses"},
	}},
	"@SCN-RNF09-004": {DefaultCase: {
		"context_and_deadline":   {"traceparent", "grpc_max_deadline"},
		"crossed_grpc":           {"trace_id", "span_kinds"},
		"traceparent_correlated": {"trace_id", "caller_span_id", "lineage"},
		"limits_enforced":        {"deadline_code", "cancel_code", "oversize_code"},
		"telemetry_redacted":     {"inspected_containers", "sensitive_values_checked", "matches"},
	}},
}

var invalidIdentityOracles = map[string][]string{
	"condition_exercised":     {"credential_state"},
	"protected_operation":     {"method", "path"},
	"rejected":                {"edge_status"},
	"no_effect_or_disclosure": {"entrypoint_delta", "authenticated_delta", "identifiers_disclosed"},
}

var edgeForwardingOracles = map[string][]string{
	"condition_exercised":      {"credential_state"},
	"public_edge_call":         {"edge_image", "method", "path"},
	"rejected_without_forward": {"edge_status", "entrypoint_before", "entrypoint_after", "forwarding_control"},
}

// NewKey mints the ephemeral attestation key. It never reaches disk: the
// verifying process keeps it in memory and passes it to the E2E it spawns.
func NewKey() ([]byte, error) {
	key := make([]byte, keyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("runtimeevidence: mint attestation key: %w", err)
	}
	return key, nil
}

// WriteKeyFile mints a fresh attestation key and writes it, hex-encoded, to a
// private (0600) temporary file. It returns the file's path, the only thing
// meant to travel as an environment value or command-line argument, the
// key bytes themselves, and a cleanup function the caller must run once the
// key is no longer needed. The key is never printed, logged or passed
// directly as an argument or an echoed variable value.
func WriteKeyFile() (path string, key []byte, cleanup func(), err error) {
	key, err = NewKey()
	if err != nil {
		return "", nil, nil, err
	}
	file, err := os.CreateTemp("", "cashflow-evidence-key-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("runtimeevidence: create key file: %w", err)
	}
	cleanup = func() { _ = os.Remove(file.Name()) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, nil, fmt.Errorf("runtimeevidence: restrict key file permissions: %w", err)
	}
	if _, err := file.WriteString(hex.EncodeToString(key)); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, nil, fmt.Errorf("runtimeevidence: write key file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("runtimeevidence: close key file: %w", err)
	}
	return file.Name(), key, cleanup, nil
}

// KeyFromEnv reads the attestation key from the private file named by
// KeyFileEnvVar. Only a file path ever crosses this boundary; the key bytes
// are read from disk exactly once and never appear in any argument list or
// echoed variable value.
func KeyFromEnv() ([]byte, error) {
	path := os.Getenv(KeyFileEnvVar)
	if path == "" {
		return nil, fmt.Errorf("runtimeevidence: %s is not set", KeyFileEnvVar)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("runtimeevidence: read %s: %w", KeyFileEnvVar, err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(contents)))
	if err != nil || len(key) != keyBytes {
		return nil, fmt.Errorf("runtimeevidence: %s must contain %d hex-encoded bytes", KeyFileEnvVar, keyBytes)
	}
	return key, nil
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

// Observe records one exact oracle with the values measured against the running
// stack. Unknown scenario/case/oracle combinations, missing observation keys and
// empty values are rejected, so a generic list or counter cannot satisfy Godog.
func (e *Evidence) Observe(tag, caseID, oracle, detail string, observations map[string]string) error {
	keys, ok := requiredKeys(tag, caseID, oracle)
	if !ok {
		return fmt.Errorf("runtimeevidence: unknown oracle %s/%s/%s", tag, caseID, oracle)
	}
	if strings.TrimSpace(detail) == "" {
		return errors.New("runtimeevidence: oracle detail is required")
	}
	if err := checkObservations(tag, caseID, oracle, keys, observations); err != nil {
		return err
	}
	scenario := e.Scenarios[tag]
	if scenario.Cases == nil {
		scenario.Cases = make(map[string]Case)
	}
	testCase := scenario.Cases[caseID]
	if testCase.Oracles == nil {
		testCase.Oracles = make(map[string]Oracle)
	}
	recorded := make(map[string]string, len(observations))
	for key, value := range observations {
		recorded[key] = value
	}
	testCase.Oracles[oracle] = Oracle{Detail: detail, Observations: recorded}
	scenario.Cases[caseID] = testCase
	e.Scenarios[tag] = scenario
	return nil
}

// Require is the literal Godog oracle. Every step names the exact result it
// needs; a result belonging to another example or scenario cannot satisfy it.
func Require(e Evidence, tag, caseID, oracle string) (Oracle, error) {
	keys, ok := requiredKeys(tag, caseID, oracle)
	if !ok {
		return Oracle{}, fmt.Errorf("runtimeevidence: binding requested unknown oracle %s/%s/%s", tag, caseID, oracle)
	}
	result, present := e.Scenarios[tag].Cases[caseID].Oracles[oracle]
	if !present {
		return Oracle{}, fmt.Errorf("runtimeevidence: required oracle is absent: %s/%s/%s", tag, caseID, oracle)
	}
	if strings.TrimSpace(result.Detail) == "" {
		return Oracle{}, fmt.Errorf("runtimeevidence: oracle %s/%s/%s carries no detail", tag, caseID, oracle)
	}
	if err := checkObservations(tag, caseID, oracle, keys, result.Observations); err != nil {
		return Oracle{}, err
	}
	return result, nil
}

// RequireValue is the strictest binding form: the step names both the oracle and
// the exact value the runtime must have observed.
func RequireValue(e Evidence, tag, caseID, oracle, key, want string) error {
	result, err := Require(e, tag, caseID, oracle)
	if err != nil {
		return err
	}
	if got := result.Observations[key]; got != want {
		return fmt.Errorf("runtimeevidence: %s/%s/%s observed %s=%q, want %q", tag, caseID, oracle, key, got, want)
	}
	return nil
}

func checkObservations(tag, caseID, oracle string, keys []string, observations map[string]string) error {
	if len(observations) != len(keys) {
		return fmt.Errorf("runtimeevidence: %s/%s/%s carries %d observations, want exactly %v",
			tag, caseID, oracle, len(observations), keys)
	}
	for _, key := range keys {
		value, present := observations[key]
		if !present {
			return fmt.Errorf("runtimeevidence: %s/%s/%s is missing observation %q", tag, caseID, oracle, key)
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("runtimeevidence: %s/%s/%s observed an empty %q", tag, caseID, oracle, key)
		}
	}
	return nil
}

// Load verifies provenance, freshness, completeness and the keyed attestation.
func Load(path string, key []byte) (Evidence, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Evidence{}, err
	}
	var evidence Evidence
	if err := json.Unmarshal(contents, &evidence); err != nil {
		return Evidence{}, err
	}
	if err := validate(evidence, time.Now().UTC(), key); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

// LoadForSource additionally binds the evidence to the exact checked-out tree.
// Any source edit makes a prior run unusable and forces a new real E2E.
func LoadForSource(path, repositoryRoot string, key []byte) (Evidence, error) {
	evidence, err := Load(path, key)
	if err != nil {
		return Evidence{}, err
	}
	digest, err := SourceDigest(repositoryRoot)
	if err != nil {
		return Evidence{}, err
	}
	if evidence.SourceDigest != digest {
		fmt.Printf("LoadForSource failed: evidence.SourceDigest=%s, digest=%s\n", evidence.SourceDigest, digest)
		return Evidence{}, errors.New("runtimeevidence: evidence belongs to a different source tree")
	}
	return evidence, nil
}

func Write(path string, evidence Evidence, key []byte) error {
	evidence.Integrity = ""
	evidence.Attestation = ""
	if err := validate(evidence, evidence.GeneratedAt, nil); err != nil {
		return err
	}
	if len(key) != keyBytes {
		return fmt.Errorf("runtimeevidence: attestation key must be %d bytes", keyBytes)
	}
	canonical, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	evidence.Integrity = hex.EncodeToString(digest[:])
	evidence.Attestation = attest(canonical, key)
	contents, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(contents, '\n'), 0o600)
}

// validate checks structure and freshness. When key is nil only the structural
// half runs, which is what Write needs before it can attest anything.
func validate(e Evidence, now time.Time, key []byte) error {
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
			for oracle := range oracles {
				if _, err := Require(e, tag, caseID, oracle); err != nil {
					return err
				}
			}
		}
	}
	if key == nil {
		return nil
	}
	return verifyAttestation(e, key)
}

// verifyAttestation recomputes both digests over the canonical form the writer
// signed. The public SHA-256 reports accidental corruption; only the keyed MAC
// decides whether the file came from the run this process started.
func verifyAttestation(e Evidence, key []byte) error {
	provided, attestation := e.Integrity, e.Attestation
	e.Integrity, e.Attestation = "", ""
	canonical, err := json.Marshal(e)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	if provided != hex.EncodeToString(digest[:]) {
		return errors.New("runtimeevidence: integrity check failed")
	}
	if subtle.ConstantTimeCompare([]byte(attestation), []byte(attest(canonical, key))) != 1 {
		return errors.New("runtimeevidence: attestation does not match this run's key; the evidence was not produced by the real E2E started here")
	}
	return nil
}

func attest(canonical, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil))
}

func requiredKeys(tag, caseID, oracle string) ([]string, bool) {
	keys, ok := required[tag][caseID][oracle]
	return keys, ok
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

// RequiredMatrix reports the exact scenario/case/oracle/observation shape a real
// run must produce, so the catalog test can assert it without duplicating it.
func RequiredMatrix() map[string]map[string]map[string][]string {
	return required
}
