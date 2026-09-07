package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Client is a thin wrapper around the native ClickHouse connection.
type Client struct {
	driver.Conn
}

// New opens a native-protocol connection and pings it.
func New(ctx context.Context, cfg Config) (*Client, error) {
	conn, err := clickhouse.Open(options(cfg))
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}
	return &Client{Conn: conn}, nil
}

func options(cfg Config) *clickhouse.Options {
	return &clickhouse.Options{
		Addr:        []string{cfg.Addr},
		Auth:        clickhouse.Auth{Database: cfg.Database, Username: cfg.User, Password: cfg.Password},
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		DialTimeout: 5 * time.Second,
	}
}
