package redact_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/higordiegoti/keyrus/internal/platform/observability/redact"
)

const sampleJWT = "eyJhbGciOiJSUzI1NiIsImtpZCI6ImsxIn0.eyJzdWIiOiJtZXJjaGFudC1hIn0.c2lnbmF0dXJlLXZhbHVl"

func TestSensitiveAttributeValuesNeverSurvive(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		"authorization":   "Bearer " + sampleJWT,
		"idempotency-key": "0f0e5c1a-2b3c-4d5e-8f90-a1b2c3d4e5f6",
		"description":     "pagamento do fornecedor Jose",
		"amount":          "100.00",
		"amount_minor":    "10000",
		"closing_balance": "-30.00",
		"trace_id":        "4bf92f3577b34da6a3ce929d0e0e4736",
		"entry_id":        "entry-1",
	}

	safe := redact.Map(values)
	for _, name := range []string{"authorization", "idempotency-key", "description", "amount", "amount_minor", "closing_balance"} {
		if safe[name] != redact.Placeholder {
			t.Errorf("%s survived redaction as %q", name, safe[name])
		}
	}
	for _, name := range []string{"trace_id", "entry_id"} {
		if safe[name] != values[name] {
			t.Errorf("correlation field %s was altered: %q", name, safe[name])
		}
	}
	if values["authorization"] == redact.Placeholder {
		t.Error("redaction mutated the caller's map")
	}
}

func TestCredentialMaterialIsStrippedFromFreeText(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"bearer header echoed into a message": "upstream rejected Authorization: Bearer " + sampleJWT,
		"bare compact JWS":                    "token " + sampleJWT + " could not be parsed",
		"basic credential":                    "proxy said Basic dXNlcjpwYXNzd29yZA==",
	}
	for name, message := range cases {
		t.Run(name, func(t *testing.T) {
			scrubbed := redact.String(message)
			if strings.Contains(scrubbed, sampleJWT) || strings.Contains(scrubbed, "dXNlcjpwYXNzd29yZA==") {
				t.Fatalf("credential material survived: %q", scrubbed)
			}
			if !strings.Contains(scrubbed, redact.Placeholder) {
				t.Fatalf("no placeholder was written: %q", scrubbed)
			}
		})
	}
}

func TestPseudonymizerIsStableIrreversibleAndSaltDependent(t *testing.T) {
	t.Parallel()
	merchantID := "11111111-1111-4111-8111-111111111111"
	first := redact.NewPseudonymizer([]byte("salt-one"))
	second := redact.NewPseudonymizer([]byte("salt-two"))

	label := first.MerchantID(merchantID)
	if label != first.MerchantID(merchantID) {
		t.Error("the same merchant produced two different labels")
	}
	if strings.Contains(label, merchantID) {
		t.Errorf("the label leaks the merchant identifier: %q", label)
	}
	if label == second.MerchantID(merchantID) {
		t.Error("two deployments with different salts produced the same label")
	}
	if first.MerchantID("") != "" {
		t.Error("an absent merchant was disguised as a real one")
	}
}

func TestLogHandlerRedactsRecordsAndGroups(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := slog.New(redact.NewHandler(slog.NewJSONHandler(&buffer, nil)))

	logger.With(slog.String("authorization", "Bearer "+sampleJWT)).Info(
		"entry rejected for Bearer "+sampleJWT,
		slog.String("entry_id", "entry-1"),
		slog.Group("request",
			slog.String("idempotency-key", "key-1"),
			slog.String("description", "compra de insumos"),
		),
		slog.Int("status", 403),
	)

	output := buffer.String()
	for _, forbidden := range []string{sampleJWT, "key-1", "compra de insumos"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("log output leaked %q: %s", forbidden, output)
		}
	}

	var record map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if record["entry_id"] != "entry-1" {
		t.Errorf("correlation field was dropped: %v", record["entry_id"])
	}
	if record["status"] != float64(403) {
		t.Errorf("non sensitive value was altered: %v", record["status"])
	}
}

func TestLogHandlerDelegatesLevelDecisions(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	inner := slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelWarn})
	handler := redact.NewHandler(inner)

	if handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info was enabled although the wrapped handler filters below warn")
	}
	if !handler.Enabled(context.Background(), slog.LevelError) {
		t.Error("error was disabled although the wrapped handler allows it")
	}
}
