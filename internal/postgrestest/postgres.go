// Package postgrestest starts the mandatory real PostgreSQL used by integration
// and BDD gates. Startup failures are returned to callers and must fail closed.
package postgrestest

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const Image = "postgres:18-alpine@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15"

type Instance struct {
	DSN           string
	Pool          *pgxpool.Pool
	containerName string
}

func Start(ctx context.Context, suite string) (*Instance, error) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	instance := &Instance{DSN: dsn}
	if dsn == "" {
		if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
			return nil, fmt.Errorf("PostgreSQL integration requires Docker or TEST_POSTGRES_DSN: %w", err)
		}
		instance.containerName = "keyrus-" + suite + "-postgres-" + strconv.Itoa(os.Getpid())
		output, err := exec.CommandContext(ctx, "docker", "run", "--detach", "--rm",
			"--name", instance.containerName,
			"--env", "POSTGRES_PASSWORD=postgres",
			"--env", "POSTGRES_DB=cashflow",
			"--publish", "127.0.0.1::5432",
			Image).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("start pinned PostgreSQL container %s: %w (%s)", Image, err, strings.TrimSpace(string(output)))
		}
		portOutput, err := exec.CommandContext(ctx, "docker", "port", instance.containerName, "5432/tcp").Output()
		if err != nil {
			instance.stopContainer()
			return nil, fmt.Errorf("resolve PostgreSQL container port: %w", err)
		}
		_, port, err := net.SplitHostPort(strings.TrimSpace(string(portOutput)))
		if err != nil {
			instance.stopContainer()
			return nil, fmt.Errorf("parse PostgreSQL container port: %w", err)
		}
		instance.DSN = "postgres://postgres:postgres@127.0.0.1:" + port + "/cashflow?sslmode=disable"
	}

	deadline := time.Now().Add(45 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		instance.Pool, err = pgxpool.New(ctx, instance.DSN)
		if err == nil {
			err = instance.Pool.Ping(ctx)
		}
		if err == nil {
			return instance, nil
		}
		if instance.Pool != nil {
			instance.Pool.Close()
			instance.Pool = nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	instance.stopContainer()
	return nil, fmt.Errorf("connect to mandatory PostgreSQL integration database: %w", err)
}

func (instance *Instance) Close() {
	if instance == nil {
		return
	}
	if instance.Pool != nil {
		instance.Pool.Close()
	}
	instance.stopContainer()
}

func (instance *Instance) stopContainer() {
	if instance.containerName != "" {
		_ = exec.Command("docker", "rm", "--force", instance.containerName).Run()
		instance.containerName = ""
	}
}
