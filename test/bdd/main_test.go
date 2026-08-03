package bdd_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/higordiegoti/keyrus/internal/postgrestest"
)

var bddDatabase *postgrestest.Instance

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), postgrestest.StartupTimeout)
	database, err := postgrestest.Start(ctx, "bdd")
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("TEST_POSTGRES_DSN", database.DSN); err != nil {
		_ = database.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bddDatabase = database
	fmt.Fprintf(os.Stderr, "BDD PostgreSQL ready: %s database=%s\n", postgrestest.Image, database.DatabaseName)
	code := m.Run()
	if err := database.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

func TestBDDDatabaseIsOwnedByThisPackageProcess(t *testing.T) {
	var current string
	if err := bddDatabase.Pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	wantPrefix := "keyrus_bdd_" + strconv.Itoa(os.Getpid()) + "_"
	if current != bddDatabase.DatabaseName || !strings.HasPrefix(current, wantPrefix) {
		t.Fatalf("BDD database %q is not owned by this package/process (%q)", current, wantPrefix)
	}
}
