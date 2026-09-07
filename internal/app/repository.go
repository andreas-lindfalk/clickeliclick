package app

import (
	"context"
	"fmt"
	"time"

	"clickeliclick/internal/pkg/clickhouse"

	cl "github.com/ClickHouse/clickhouse-go/v2"
)

type Repository struct {
	client *clickhouse.Client
}

func NewRepository(client *clickhouse.Client) *Repository {
	return &Repository{client: client}
}

// InsertEvents writes rows using a batch, which is the idiomatic way to insert
// into ClickHouse. Prefer fewer, larger batches over many single-row inserts.
func (r *Repository) InsertEvents(ctx context.Context, events []Event) error {
	batch, err := r.client.PrepareBatch(ctx, "INSERT INTO events (ts, user_id, event_type, payload)")
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
func (r *Repository) RecentEvents(ctx context.Context, n int) ([]Event, error) {
	rows, err := r.client.Query(ctx,
		"SELECT ts, user_id, event_type, payload FROM events ORDER BY ts DESC LIMIT {n:UInt32}",
		cl.Named("n", uint32(n)),
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
func (r *Repository) CountByType(ctx context.Context) (map[string]uint64, error) {
	rows, err := r.client.Query(ctx, "SELECT event_type, count() FROM events GROUP BY event_type ORDER BY event_type")
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
