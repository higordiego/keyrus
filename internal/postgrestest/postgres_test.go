package postgrestest

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestUniqueDatabaseNameIdentifiesSuiteAndProcess(t *testing.T) {
	first, err := uniqueDatabaseName("BDD package")
	if err != nil {
		t.Fatal(err)
	}
	second, err := uniqueDatabaseName("BDD package")
	if err != nil {
		t.Fatal(err)
	}
	prefix := "keyrus_bdd_package_" + strconv.Itoa(os.Getpid()) + "_"
	if !strings.HasPrefix(first, prefix) || !strings.HasPrefix(second, prefix) {
		t.Fatalf("database names are not suite/process identifiable: %q %q", first, second)
	}
	if first == second {
		t.Fatalf("isolated database names collided: %q", first)
	}
	if len(first) > 63 || len(second) > 63 {
		t.Fatalf("database name exceeds PostgreSQL identifier limit")
	}
}

func TestUniqueDatabaseNameKeepsRandomSuffixWhenSuiteIsLong(t *testing.T) {
	longSuite := strings.Repeat("a", 100)
	first, err := uniqueDatabaseName(longSuite)
	if err != nil {
		t.Fatal(err)
	}
	second, err := uniqueDatabaseName(longSuite)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) > 63 || len(second) > 63 {
		t.Fatalf("long suite lost bounded uniqueness: %q %q", first, second)
	}
}

func TestSanitizeNameRejectsEmptyIdentity(t *testing.T) {
	if got := sanitizeName("Store / PostgreSQL"); got != "store_postgresql" {
		t.Fatalf("sanitize suite name = %q", got)
	}
	if got := sanitizeName("---"); got != "" {
		t.Fatalf("punctuation-only suite name = %q", got)
	}
}

func TestDSNWithDatabaseOverridesURLAndKeywordFormats(t *testing.T) {
	for name, base := range map[string]string{
		"url":     "postgres://user:secret@localhost:5432/base?sslmode=disable&application_name=test",
		"keyword": "host=localhost port=5432 user=user password=secret dbname=base sslmode=disable",
	} {
		t.Run(name, func(t *testing.T) {
			derived, err := dsnWithDatabase(base, "keyrus_isolated_123")
			if err != nil {
				t.Fatal(err)
			}
			config, err := pgx.ParseConfig(derived)
			if err != nil {
				t.Fatal(err)
			}
			if config.Database != "keyrus_isolated_123" {
				t.Fatalf("derived database = %q", config.Database)
			}
		})
	}
}
