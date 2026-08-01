package migrations

import (
	"strings"
	"testing"
)

func TestProjectionMigrationIsEmbeddedAndSchemaQualified(t *testing.T) {
	contents, err := FS.ReadFile("000001_consolidation_projection.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, table := range []string{
		"inbox_event", "daily_balance", "merchant_progress", "position_receipt",
		"recompute_job", "event_pending", "dead_letter_event",
	} {
		if !strings.Contains(sql, "consolidation."+table) {
			t.Errorf("migration does not schema-qualify %s", table)
		}
	}
}

func TestRecomputeContinuationMigrationIsEmbedded(t *testing.T) {
	contents, err := FS.ReadFile("000002_recompute_continuation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, required := range []string{"next_date", "recompute_job_continuation_check", "status = 'pending'"} {
		if !strings.Contains(sql, required) {
			t.Errorf("continuation migration does not contain %q", required)
		}
	}
}
