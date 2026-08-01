package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	consolidationpostgres "github.com/higordiegoti/keyrus/services/consolidation/internal/adapters/outbound/postgres"
)

func main() {
	databaseURL := flag.String("database-url", os.Getenv("CONSOLIDATION_DATABASE_URL"), "PostgreSQL connection URL (or set CONSOLIDATION_DATABASE_URL)")
	timeout := flag.Duration("timeout", 30*time.Second, "migration timeout")
	flag.Parse()
	if *databaseURL == "" {
		fmt.Fprintln(os.Stderr, "CONSOLIDATION_DATABASE_URL or -database-url is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, *databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configure consolidation database:", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "connect to consolidation database:", err)
		os.Exit(1)
	}
	if err := consolidationpostgres.ApplyMigrations(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("consolidation migrations applied")
}
