// Package clickhousetest starts a throwaway ClickHouse in Docker for
// integration tests, following the net/http/httptest convention. It is a real
// server, so keep it out of production imports.
package clickhousetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"

	"clickeliclick/internal/pkg/clickhouse"
)

const image = "clickhouse/clickhouse-server:26.8"

// cfg is completed with the container's address once it is running. The
// default user has no password, as in docker-compose.yml. The image only
// allows that when CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT is set; otherwise it
// disables network access for a passwordless default user. A passwordless
// default user also lets the users_dict dictionary (migration 006) read its
// source table without credentials in the migration.
var cfg = clickhouse.Config{Database: "poc", User: "default", Password: ""}

// Server is a ClickHouse container with the project migrations applied.
type Server struct {
	Container *tcclickhouse.ClickHouseContainer
	cfg       clickhouse.Config
}

// Start runs the container and waits until it accepts connections.
func Start(ctx context.Context) (*Server, error) {
	ctr, err := tcclickhouse.Run(ctx, image,
		tcclickhouse.WithDatabase(cfg.Database),
		tcclickhouse.WithUsername(cfg.User),
		tcclickhouse.WithPassword(cfg.Password),
		testcontainers.WithEnv(map[string]string{"CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1"}),
	)
	if err != nil {
		return nil, fmt.Errorf("run clickhouse container: %w", err)
	}

	addr, err := ctr.ConnectionHost(ctx)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("connection host: %w", err),
			testcontainers.TerminateContainer(ctr),
		)
	}

	c := cfg
	c.Addr = addr
	if err := clickhouse.Migrate(ctx, c); err != nil {
		return nil, errors.Join(err, testcontainers.TerminateContainer(ctr))
	}

	return &Server{Container: ctr, cfg: c}, nil
}

// NewClient connects to the server and truncates every data table so each
// test starts from a clean slate. Materialized views are not tables and keep
// working; goose's version table is left alone.
func (s *Server) NewClient(t *testing.T) *clickhouse.Client {
	t.Helper()
	ctx := context.Background()

	c, err := clickhouse.New(ctx, s.cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })

	rows, err := c.Query(ctx, `SELECT name FROM system.tables
		WHERE database = currentDatabase() AND engine LIKE '%MergeTree' AND name != 'goose_db_version'`)
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Close())
	for _, name := range tables {
		require.NoError(t, c.Exec(ctx, "TRUNCATE TABLE "+name))
	}
	// Dictionaries cache their source; drop what the previous test loaded.
	require.NoError(t, c.Exec(ctx, "SYSTEM RELOAD DICTIONARIES"))
	return c
}

// Close terminates the container. Call it explicitly from TestMain; a defer
// will not run if TestMain exits through os.Exit.
func (s *Server) Close() error {
	if s == nil || s.Container == nil {
		return nil
	}
	return testcontainers.TerminateContainer(s.Container)
}

// Config returns the connection details for the default user, so tests can
// derive connections for other users (see migration 007).
func (s *Server) Config() clickhouse.Config {
	return s.cfg
}
