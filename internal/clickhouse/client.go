package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Event is a single row in poc.events.
type Event struct {
	TS        time.Time `json:"ts"`
	UserID    uint64    `json:"user_id"`
	EventType string    `json:"event_type"`
	Payload   string    `json:"payload"`
}

// Client is a thin wrapper around the native ClickHouse connection.
type Client struct {
	conn driver.Conn
}

// New opens a native-protocol connection and pings it.
func New(ctx context.Context, addr, database, user, password string) (*Client, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Database: database, Username: user, Password: password},
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}
	return &Client{conn: conn}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Exec runs a statement that returns no rows (DDL, TRUNCATE, etc).
func (c *Client) Exec(ctx context.Context, query string, args ...any) error {
	return c.conn.Exec(ctx, query, args...)
}

// InsertEvents writes rows using a batch, which is the idiomatic way to insert
// into ClickHouse. Prefer fewer, larger batches over many single-row inserts.
func (c *Client) InsertEvents(ctx context.Context, events []Event) error {
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO events (ts, user_id, event_type, payload)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, e := range events {
		ts := e.TS
		if ts.IsZero() {
			ts = time.Now()
		}
		if err := batch.Append(ts, e.UserID, e.EventType, e.Payload); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	return batch.Send()
}

// RecentEvents returns the latest n rows.
func (c *Client) RecentEvents(ctx context.Context, n int) ([]Event, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT ts, user_id, event_type, payload FROM events ORDER BY ts DESC LIMIT {n:UInt32}",
		clickhouse.Named("n", uint32(n)),
	)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.TS, &e.UserID, &e.EventType, &e.Payload); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountByType is a small aggregation example.
func (c *Client) CountByType(ctx context.Context) (map[string]uint64, error) {
	rows, err := c.conn.Query(ctx, "SELECT event_type, count() FROM events GROUP BY event_type ORDER BY event_type")
	if err != nil {
		return nil, fmt.Errorf("query counts: %w", err)
	}
	defer rows.Close()

	out := map[string]uint64{}
	for rows.Next() {
		var t string
		var n uint64
		if err := rows.Scan(&t, &n); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		out[t] = n
	}
	return out, rows.Err()
}
