// Package runtimeevidence carries machine-readable results from the real T02
// container stack into the Godog bindings. Evidence is never synthesized: the
// integration test writes it only after every runtime oracle passes.
package runtimeevidence

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

type Scenario struct {
	Assertions []string `json:"assertions"`
}

type Evidence struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Runtime     string              `json:"runtime"`
	Scenarios   map[string]Scenario `json:"scenarios"`
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
	if evidence.Runtime != "keycloak+krakend+ledger-image+consolidation-image+otel-collector" || evidence.GeneratedAt.IsZero() {
		return Evidence{}, errors.New("runtimeevidence: evidence provenance is invalid")
	}
	return evidence, nil
}

func Write(path string, evidence Evidence) error {
	contents, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(contents, '\n'), 0o600)
}
