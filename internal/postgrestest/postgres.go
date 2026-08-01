// Package postgrestest provisions the mandatory isolated PostgreSQL database
// used by integration and BDD gates. Startup and provisioning fail closed.
package postgrestest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const Image = "postgres:18-alpine@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15"

// StartupTimeout is deliberately larger than the roughly two-minute cold
// start observed for the pinned image on the supported development host.
const StartupTimeout = 5 * time.Minute

var invalidDatabaseName = regexp.MustCompile(`[^a-z0-9_]+`)

type Instance struct {
	DSN           string
	Pool          *pgxpool.Pool
	DatabaseName  string
	adminDSN      string
	containerName string
}

func Start(ctx context.Context, suite string) (*Instance, error) {
	suite = sanitizeName(suite)
	if suite == "" {
		return nil, errors.New("PostgreSQL integration suite name is required")
	}
	instance := &Instance{}
	baseDSN := os.Getenv("TEST_POSTGRES_DSN")
	if baseDSN == "" {
		if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
			return nil, fmt.Errorf("PostgreSQL integration requires Docker or TEST_POSTGRES_DSN: %w", err)
		}
		instance.containerName = "keyrus-" + strings.ReplaceAll(suite, "_", "-") + "-postgres-" + strconv.Itoa(os.Getpid())
		output, err := exec.CommandContext(ctx, "docker", "run", "--detach", "--rm",
			"--name", instance.containerName,
			"--env", "POSTGRES_PASSWORD=postgres",
			"--publish", "127.0.0.1::5432",
			Image).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("start pinned PostgreSQL container %s: %w (%s)", Image, err, strings.TrimSpace(string(output)))
		}
		portOutput, err := exec.CommandContext(ctx, "docker", "port", instance.containerName, "5432/tcp").Output()
		if err != nil {
			_ = instance.stopContainer()
			return nil, fmt.Errorf("resolve PostgreSQL container port: %w", err)
		}
		_, port, err := net.SplitHostPort(strings.TrimSpace(string(portOutput)))
		if err != nil {
			_ = instance.stopContainer()
			return nil, fmt.Errorf("parse PostgreSQL container port: %w", err)
		}
		baseDSN = "postgres://postgres:postgres@127.0.0.1:" + port + "/postgres?sslmode=disable"
	}

	instance.adminDSN = baseDSN
	if err := waitReady(ctx, baseDSN); err != nil {
		logs := instance.containerLogs()
		_ = instance.stopContainer()
		return nil, fmt.Errorf("wait for mandatory PostgreSQL server: %w%s", err, logs)
	}

	databaseName, err := uniqueDatabaseName(suite)
	if err != nil {
		_ = instance.stopContainer()
		return nil, err
	}
	instance.DatabaseName = databaseName
	if err := instance.createDatabase(ctx); err != nil {
		return nil, errors.Join(err, instance.cleanupOwned())
	}
	if err := instance.connectDatabase(ctx); err != nil {
		return nil, errors.Join(err, instance.cleanupOwned())
	}
	return instance, nil
}

func waitReady(ctx context.Context, dsn string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			err = pool.Ping(ctx)
			pool.Close()
		}
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("readiness deadline reached: %w (last connection error: %v)", ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func (instance *Instance) createDatabase(ctx context.Context) error {
	config, err := pgx.ParseConfig(instance.adminDSN)
	if err != nil {
		return fmt.Errorf("parse PostgreSQL administration DSN: %w", err)
	}
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("connect for isolated database provisioning: %w", err)
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	statement := "CREATE DATABASE " + pgx.Identifier{instance.DatabaseName}.Sanitize() + " TEMPLATE template0"
	if _, err := connection.Exec(ctx, statement); err != nil {
		return fmt.Errorf("create isolated PostgreSQL database %s: %w", instance.DatabaseName, err)
	}
	return nil
}

func (instance *Instance) connectDatabase(ctx context.Context) error {
	isolatedDSN, err := dsnWithDatabase(instance.adminDSN, instance.DatabaseName)
	if err != nil {
		return err
	}
	instance.DSN = isolatedDSN
	instance.Pool, err = pgxpool.New(ctx, instance.DSN)
	if err != nil {
		return fmt.Errorf("open isolated PostgreSQL database %s: %w", instance.DatabaseName, err)
	}
	if err := instance.Pool.Ping(ctx); err != nil {
		instance.Pool.Close()
		instance.Pool = nil
		return fmt.Errorf("connect isolated PostgreSQL database %s: %w", instance.DatabaseName, err)
	}
	return nil
}

func dsnWithDatabase(baseDSN, databaseName string) (string, error) {
	if strings.HasPrefix(baseDSN, "postgres://") || strings.HasPrefix(baseDSN, "postgresql://") {
		parsed, err := url.Parse(baseDSN)
		if err != nil {
			return "", fmt.Errorf("parse isolated PostgreSQL URL: %w", err)
		}
		parsed.Path = "/" + databaseName
		derived := parsed.String()
		config, err := pgx.ParseConfig(derived)
		if err != nil {
			return "", fmt.Errorf("derive isolated PostgreSQL URL for %s: %w", databaseName, err)
		}
		if config.Database != databaseName {
			return "", fmt.Errorf("derived PostgreSQL URL selected database %q instead of %q", config.Database, databaseName)
		}
		return derived, nil
	}
	escapedName := strings.ReplaceAll(databaseName, "'", "\\'")
	derived := strings.TrimSpace(baseDSN) + " dbname='" + escapedName + "'"
	config, err := pgx.ParseConfig(derived)
	if err != nil {
		return "", fmt.Errorf("derive isolated PostgreSQL connection string for %s: %w", databaseName, err)
	}
	if config.Database != databaseName {
		return "", fmt.Errorf("derived PostgreSQL connection string selected database %q instead of %q", config.Database, databaseName)
	}
	return derived, nil
}

func (instance *Instance) Close() error {
	if instance == nil {
		return nil
	}
	if instance.Pool != nil {
		instance.Pool.Close()
		instance.Pool = nil
	}
	return instance.cleanupOwned()
}

func (instance *Instance) cleanupOwned() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return errors.Join(instance.dropDatabase(ctx), instance.stopContainer())
}

func (instance *Instance) dropDatabase(ctx context.Context) error {
	if instance.DatabaseName == "" || instance.adminDSN == "" {
		return nil
	}
	config, err := pgx.ParseConfig(instance.adminDSN)
	if err != nil {
		return fmt.Errorf("parse PostgreSQL cleanup DSN: %w", err)
	}
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("connect for isolated database cleanup: %w", err)
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	statement := "DROP DATABASE IF EXISTS " + pgx.Identifier{instance.DatabaseName}.Sanitize() + " WITH (FORCE)"
	if _, err := connection.Exec(ctx, statement); err != nil {
		return fmt.Errorf("drop isolated PostgreSQL database %s: %w", instance.DatabaseName, err)
	}
	instance.DatabaseName = ""
	return nil
}

func (instance *Instance) stopContainer() error {
	if instance.containerName == "" {
		return nil
	}
	name := instance.containerName
	instance.containerName = ""
	output, err := exec.Command("docker", "rm", "--force", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove owned PostgreSQL container %s: %w (%s)", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (instance *Instance) containerLogs() string {
	if instance.containerName == "" {
		return ""
	}
	output, err := exec.Command("docker", "logs", "--tail", "80", instance.containerName).CombinedOutput()
	if err != nil {
		return fmt.Sprintf(" (container logs unavailable: %v)", err)
	}
	return "\nPostgreSQL container logs:\n" + strings.TrimSpace(string(output))
}

func uniqueDatabaseName(suite string) (string, error) {
	suite = sanitizeName(suite)
	if suite == "" {
		return "", errors.New("PostgreSQL integration suite name is required")
	}
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate isolated PostgreSQL database name: %w", err)
	}
	suffix := fmt.Sprintf("_%d_%s", os.Getpid(), hex.EncodeToString(random))
	maxSuiteLength := 63 - len("keyrus_") - len(suffix)
	if len(suite) > maxSuiteLength {
		suite = suite[:maxSuiteLength]
	}
	return "keyrus_" + suite + suffix, nil
}

func sanitizeName(value string) string {
	value = strings.ToLower(value)
	value = invalidDatabaseName.ReplaceAllString(value, "_")
	return strings.Trim(value, "_")
}
