package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"clickeliclick/internal/pkg/clickhouse"

	cl "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
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
		if err := batch.Append(ts, e.UserID, e.EventType, payloadOrEmpty(e.Payload)); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	return batch.Send()
}

// InsertEventColumns sends a whole batch with one Append per column instead of
// one per row. For bulk loads this avoids per-row reflection in the driver and
// is the fastest path into ClickHouse.
func (r *Repository) InsertEventColumns(ctx context.Context, cols EventColumns) error {
	batch, err := r.client.PrepareBatch(ctx, "INSERT INTO events (ts, user_id, event_type, payload)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for i, col := range []any{cols.TS, cols.UserID, cols.EventType, cols.Payload} {
		if err := batch.Column(i).Append(col); err != nil {
			return fmt.Errorf("append column %d: %w", i, err)
		}
	}
	return batch.Send()
}

// InsertEventAsync writes a single row using ClickHouse's server-side async
// inserts: the server buffers rows from many small inserts and flushes them as
// one part, so callers do not have to batch themselves. The async mode is
// attached to the context and turns into the async_insert setting on the wire.
// With wait=true the call returns only once the buffer has been flushed to a
// part, so a subsequent SELECT will see the row.
func (r *Repository) InsertEventAsync(ctx context.Context, e Event) error {
	ts := e.TS
	if ts.IsZero() {
		ts = time.Now()
	}
	ctx = cl.Context(ctx, cl.WithAsync(true))
	return r.client.Exec(ctx,
		"INSERT INTO events (ts, user_id, event_type, payload) VALUES (?, ?, ?, ?)",
		ts, e.UserID, e.EventType, payloadOrEmpty(e.Payload),
	)
}

// payloadOrEmpty returns the payload as the JSON text ClickHouse parses on
// insert, substituting an empty object for a missing one.
func payloadOrEmpty(p json.RawMessage) string {
	if len(p) == 0 {
		return "{}"
	}
	return string(p)
}

// scanEvents drains a result set of (ts, user_id, event_type, payload) rows.
// The JSON column arrives as text thanks to the connection setting
// output_format_native_write_json_as_string.
func scanEvents(rows driver.Rows) ([]Event, error) {
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.TS, &e.UserID, &e.EventType, &payload); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
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
	return scanEvents(rows)
}

// RecentEventsByUser returns the latest n rows for one user. Served from the
// by_user projection, which is sorted by (user_id, ts); see migration 002.
func (r *Repository) RecentEventsByUser(ctx context.Context, userID uint64, n int) ([]Event, error) {
	rows, err := r.client.Query(ctx,
		"SELECT ts, user_id, event_type, payload FROM events WHERE user_id = {user_id:UInt64} ORDER BY ts DESC LIMIT {n:UInt32}",
		cl.Named("user_id", userID),
		cl.Named("n", uint32(n)),
	)
	if err != nil {
		return nil, fmt.Errorf("query events by user: %w", err)
	}
	return scanEvents(rows)
}

// TopPagesByRef groups on a JSON path. payload.page and payload.ref are typed
// paths (see migration 004) so they behave like ordinary columns: only those
// two subcolumns are read, the rest of the document is never touched.
func (r *Repository) TopPagesByRef(ctx context.Context, ref string, n int) ([]PageCount, error) {
	rows, err := r.client.Query(ctx, `
		SELECT payload.page AS page, count() AS c
		FROM events
		WHERE payload.ref = {ref:String}
		GROUP BY page ORDER BY c DESC, page LIMIT {n:UInt32}`,
		cl.Named("ref", ref),
		cl.Named("n", uint32(n)),
	)
	if err != nil {
		return nil, fmt.Errorf("query top pages: %w", err)
	}
	defer rows.Close()

	out := []PageCount{}
	for rows.Next() {
		var p PageCount
		if err := rows.Scan(&p.Page, &p.Count); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		out = append(out, p)
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

// StatsPerMinute reads the events_per_minute rollup for [from, to).
//
// The rollup stores partial aggregate states, and the same minute can exist in
// several parts until a background merge combines them, so reading it always
// means GROUP BY the key plus the -Merge combinators. Reading the columns
// directly would return binary state blobs.
func (r *Repository) StatsPerMinute(ctx context.Context, from, to time.Time) ([]MinuteStats, error) {
	rows, err := r.client.Query(ctx, `
		SELECT minute, event_type, countMerge(events) AS events, uniqMerge(users) AS users
		FROM events_per_minute
		WHERE minute >= {from:DateTime} AND minute < {to:DateTime}
		GROUP BY minute, event_type
		ORDER BY minute, event_type`,
		cl.Named("from", from.UTC().Format(time.DateTime)),
		cl.Named("to", to.UTC().Format(time.DateTime)),
	)
	if err != nil {
		return nil, fmt.Errorf("query events_per_minute: %w", err)
	}
	defer rows.Close()

	out := []MinuteStats{}
	for rows.Next() {
		var m MinuteStats
		if err := rows.Scan(&m.Minute, &m.EventType, &m.Events, &m.Users); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
