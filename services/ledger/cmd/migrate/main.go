// Command migrate applies the Ledger's PostgreSQL migrations. It mirrors
// services/consolidation/cmd/migrate so both services follow the same
// operational convention: migrations are a separate, explicit step, never
// applied implicitly by the API process on every startup.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/higordiegoti/keyrus/services/ledger/migrations"
)

func main() {
	databaseURL := flag.String("database-url", os.Getenv("CASHFLOW_DB_URL"), "PostgreSQL connection URL (or set CASHFLOW_DB_URL)")
	timeout := flag.Duration("timeout", 30*time.Second, "migration timeout")
	flag.Parse()
	if *databaseURL == "" {
		fmt.Fprintln(os.Stderr, "CASHFLOW_DB_URL or -database-url is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, *databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configure ledger database:", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "connect to ledger database:", err)
		os.Exit(1)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("ledger migrations applied")
}
