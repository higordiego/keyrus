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
