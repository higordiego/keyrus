package bdd_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/higordiegoti/keyrus/internal/postgrestest"
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, err := postgrestest.Start(ctx, "bdd")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("TEST_POSTGRES_DSN", database.DSN); err != nil {
		database.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "BDD PostgreSQL ready: %s\n", postgrestest.Image)
	code := m.Run()
	database.Close()
	os.Exit(code)
}
