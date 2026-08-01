package events

import (
	"bytes"
	"embed"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed ledger.entry.confirmed.v1.schema.json
var schemas embed.FS

func LedgerEntryConfirmedV1Schema() (*jsonschema.Schema, error) {
	content, err := schemas.ReadFile("ledger.entry.confirmed.v1.schema.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded Ledger event schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	const location = "embedded://ledger.entry.confirmed.v1.schema.json"
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("decode embedded Ledger event schema: %w", err)
	}
	if err := compiler.AddResource(location, document); err != nil {
		return nil, fmt.Errorf("add embedded Ledger event schema: %w", err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("compile embedded Ledger event schema: %w", err)
	}
	return schema, nil
}
