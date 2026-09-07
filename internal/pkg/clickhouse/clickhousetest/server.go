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

const (
	image    = "clickhouse/clickhouse-server:26.8"
	database = "poc"
	user     = "default"
	password = "test"
)

// Server is a ClickHouse container with the project migrations applied.
type Server struct {
	Container *tcclickhouse.ClickHouseContainer

	// addr is the host:port of the container's native interface.
	addr string
}

// Start runs the container and waits until it accepts connections.
func Start(ctx context.Context) (*Server, error) {
	ctr, err := tcclickhouse.Run(ctx, image,
		tcclickhouse.WithDatabase(database),
		tcclickhouse.WithUsername(user),
		tcclickhouse.WithPassword(password),
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

	if err := clickhouse.Migrate(ctx, addr, database, user, password); err != nil {
		return nil, errors.Join(err, testcontainers.TerminateContainer(ctr))
	}

	return &Server{Container: ctr, addr: addr}, nil
}

// NewClient connects to the server and truncates the events table so each
// test starts from a clean slate.
func (s *Server) NewClient(t *testing.T) *clickhouse.Client {
	t.Helper()
	ctx := context.Background()

	c, err := clickhouse.New(ctx, s.addr, database, user, password)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })

	require.NoError(t, c.Exec(ctx, "TRUNCATE TABLE events"))
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
