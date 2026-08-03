package api_test

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	consolidationv1 "github.com/higordiegoti/keyrus/gen/go/cashflow/consolidation/public/v1"
	ledgerv1 "github.com/higordiegoti/keyrus/gen/go/cashflow/ledger/public/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type openAPIDocument struct {
	Paths       map[string]map[string]openAPIOperation `json:"paths"`
	Definitions map[string]openAPISchema               `json:"definitions"`
}

type openAPIOperation struct {
	Parameters []openAPIParameter         `json:"parameters"`
	Responses  map[string]json.RawMessage `json:"responses"`
}

type openAPIParameter struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type openAPISchema struct {
	Ref        string                   `json:"$ref"`
	Type       string                   `json:"type"`
	Enum       []string                 `json:"enum"`
	Properties map[string]openAPISchema `json:"properties"`
	XNullable  bool                     `json:"x-nullable"`
}

func loadOpenAPI(t *testing.T) openAPIDocument {
	t.Helper()
	contents, err := os.ReadFile("openapi/cashflow-public-v1.swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var document openAPIDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestPublicOpenAPIContainsOnlyEdgeRoutes(t *testing.T) {
	document := loadOpenAPI(t)
	want := []string{"/v1/daily-balances", "/v1/entries", "/v1/entries/{entryId}", "/v1/entries/{entryId}/reversals"}
	if len(document.Paths) != len(want) {
		t.Fatalf("public OpenAPI path count: got %d, want %d: %#v", len(document.Paths), len(want), document.Paths)
	}
	for _, path := range want {
		if _, exists := document.Paths[path]; !exists {
			t.Errorf("public OpenAPI is missing %s", path)
		}
	}
	for path := range document.Paths {
		if path == "/cashflow.ledger.internal.v1.LedgerInternalService/GetMerchantWatermark" || path == "/cashflow.ledger.internal.v1.LedgerInternalService/StreamEntriesAtCut" {
			t.Errorf("internal gRPC route leaked into public OpenAPI: %s", path)
		}
	}
}

func TestCommandOpenAPIRequiresIdempotencyAndDocumentsStatuses(t *testing.T) {
	document := loadOpenAPI(t)
	tests := []struct {
		path     string
		statuses []string
	}{
		{path: "/v1/entries", statuses: []string{"201", "400", "401", "403", "409"}},
		{path: "/v1/entries/{entryId}/reversals", statuses: []string{"201", "400", "401", "403", "404", "409"}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			operation, exists := document.Paths[test.path]["post"]
			if !exists {
				t.Fatalf("POST operation missing for %s", test.path)
			}
			foundHeader := false
			for _, parameter := range operation.Parameters {
				if parameter.Name == "Idempotency-Key" {
					foundHeader = parameter.In == "header" && parameter.Type == "string" && parameter.Required
				}
			}
			if !foundHeader {
				t.Fatal("required string Idempotency-Key header is missing")
			}
			for _, status := range test.statuses {
				if _, exists := operation.Responses[status]; !exists {
					t.Errorf("documented response %s is missing", status)
				}
			}
			if _, exists := operation.Responses["200"]; exists {
				t.Error("command must document 201 rather than 200")
			}
		})
	}
}

func TestReversalOpenAPIHasNoRequestBody(t *testing.T) {
	document := loadOpenAPI(t)
	reversal := document.Paths["/v1/entries/{entryId}/reversals"]["post"]
	for _, parameter := range reversal.Parameters {
		if parameter.In == "body" {
			t.Fatalf("reversal request must be path and headers only, got body parameter %#v", parameter)
		}
	}

	create := document.Paths["/v1/entries"]["post"]
	foundCreateBody := false
	for _, parameter := range create.Parameters {
		if parameter.In == "body" {
			foundCreateBody = true
		}
	}
	if !foundCreateBody {
		t.Fatal("entry creation still requires its financial request body")
	}
}

func TestDailyBalanceOpenAPIUsesNullableDataEnvelope(t *testing.T) {
	document := loadOpenAPI(t)
	envelope := document.Definitions["v1DailyBalance"]
	for _, field := range []string{"businessDate", "state", "reason", "definitive", "sourcePosition", "appliedPosition", "data"} {
		if _, exists := envelope.Properties[field]; !exists {
			t.Errorf("daily envelope is missing %s", field)
		}
	}
	for _, flattened := range []string{"credits", "debits", "net", "entryCount", "closingBalance", "snapshotAt"} {
		if _, exists := envelope.Properties[flattened]; exists {
			t.Errorf("financial snapshot field %s must be nested under data", flattened)
		}
	}
	if data := envelope.Properties["data"]; data.Ref != "#/definitions/v1DailyBalanceData" || !data.XNullable {
		t.Fatalf("data must be a nullable DailyBalanceData reference: %#v", data)
	}
	payload := document.Definitions["v1DailyBalanceData"]
	for _, field := range []string{"credits", "debits", "net", "entryCount", "closingBalance", "snapshotAt"} {
		if _, exists := payload.Properties[field]; !exists {
			t.Errorf("daily data payload is missing %s", field)
		}
	}
	operation := document.Paths["/v1/daily-balances"]["get"]
	if _, exists := operation.Responses["200"]; !exists {
		t.Error("known pending date with data null must remain a successful response")
	}
	if _, exists := operation.Responses["503"]; !exists {
		t.Error("inaccessible or unprovable snapshot must be documented as unavailable")
	}
}

func TestDailyBalanceSerializesNullAndPresentDataWithoutLosingEnvelope(t *testing.T) {
	pending := &consolidationv1.DailyBalance{
		BusinessDate:    "2026-07-31",
		SourcePosition:  4,
		AppliedPosition: 3,
		State:           consolidationv1.ConsolidationState_CONSOLIDATION_STATE_PROCESSING,
		Reason:          "pending_snapshot",
		Definitive:      false,
		Data:            nil,
	}
	encoded, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	if value, exists := object["data"]; !exists || value != nil {
		t.Fatalf("pending envelope must serialize data:null, got %s", encoded)
	}
	for _, field := range []string{"businessDate", "state", "reason", "definitive", "sourcePosition", "appliedPosition"} {
		if _, exists := object[field]; !exists {
			t.Errorf("pending envelope lost %s: %s", field, encoded)
		}
	}

	withSnapshot := &consolidationv1.DailyBalance{
		BusinessDate:    "2026-07-31",
		SourcePosition:  4,
		AppliedPosition: 4,
		State:           consolidationv1.ConsolidationState_CONSOLIDATION_STATE_UPDATED,
		Definitive:      true,
		Data: &consolidationv1.DailyBalanceData{
			Credits:        "100.00",
			Debits:         "30.00",
			Net:            "70.00",
			EntryCount:     2,
			ClosingBalance: "70.00",
		},
	}
	encoded, err = (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(withSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	if data, ok := object["data"].(map[string]any); !ok || data["net"] != "70.00" {
		t.Fatalf("snapshot envelope must serialize data object, got %s", encoded)
	}
}

func TestLedgerEntryReadProjectionExposesStateAndReversalReferences(t *testing.T) {
	wantEnum := map[string]int32{
		"ENTRY_STATE_UNSPECIFIED": 0,
		"ENTRY_STATE_CONFIRMED":   1,
		"ENTRY_STATE_REVERSED":    2,
	}
	for name, value := range wantEnum {
		if got, exists := ledgerv1.EntryState_value[name]; !exists || got != value {
			t.Errorf("EntryState %s: got %d/%v, want %d", name, got, exists, value)
		}
	}

	document := loadOpenAPI(t)
	entry := document.Definitions["v1LedgerEntry"]
	for _, field := range []string{"state", "originalEntryId", "reversalEntryId"} {
		if _, exists := entry.Properties[field]; !exists {
			t.Errorf("LedgerEntry OpenAPI is missing %s", field)
		}
	}
	enumValues := append([]string(nil), document.Definitions["v1EntryState"].Enum...)
	sort.Strings(enumValues)
	wantValues := []string{"ENTRY_STATE_CONFIRMED", "ENTRY_STATE_REVERSED", "ENTRY_STATE_UNSPECIFIED"}
	if stringList(enumValues) != stringList(wantValues) {
		t.Fatalf("EntryState OpenAPI enum: got %v, want %v", enumValues, wantValues)
	}

	// This verifies the read contract only. Runtime tickets must derive this view
	// from the compensating entry without updating the stored original record.
	original := &ledgerv1.LedgerEntry{EntryId: "original", State: ledgerv1.EntryState_ENTRY_STATE_REVERSED, ReversalEntryId: "compensation"}
	compensation := &ledgerv1.LedgerEntry{EntryId: "compensation", State: ledgerv1.EntryState_ENTRY_STATE_CONFIRMED, OriginalEntryId: "original"}
	for _, message := range []*ledgerv1.LedgerEntry{original, compensation} {
		encoded, err := protojson.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) == 0 {
			t.Fatal("LedgerEntry state serialization was empty")
		}
	}
}

func stringList(values []string) string {
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func TestEventSchemaIsVersionedAndValidJSON(t *testing.T) {
	contents, err := os.ReadFile("events/ledger.entry.confirmed.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(contents, &schema); err != nil {
		t.Fatal(err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("event schema has no properties object")
	}
	eventType, ok := properties["event_type"].(map[string]any)
	if !ok || eventType["const"] != "ledger.entry.confirmed.v1" {
		t.Fatal("event schema must pin event_type to ledger.entry.confirmed.v1")
	}
}
