package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// DeleteUserEvents removes every event for one user, the shape of an erasure
// request. It is a classic mutation: every part that contains the user is
// rewritten without those rows, and the by_user projection is rebuilt with
// it, so the data is physically gone when this returns. That is the right
// tool for rare, must-be-thorough deletes. The lightweight DELETE FROM only
// masks rows and is refused on this table (see migration 005).
//
// The events_per_minute rollup is not touched; a materialized view only ever
// sees inserts.
func (r *Repository) DeleteUserEvents(ctx context.Context, userID uint64) error {
	return r.client.Exec(ctx,
		"ALTER TABLE events DELETE WHERE user_id = {user_id:UInt64} SETTINGS mutations_sync = 1",
		cl.Named("user_id", userID),
	)
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

// UpsertUsers writes one version per user. It is a plain insert; the
// ReplacingMergeTree engine drops older versions of the same user_id when
// parts merge, and reads use FINAL until then.
func (r *Repository) UpsertUsers(ctx context.Context, users []User) error {
	batch, err := r.client.PrepareBatch(ctx, "INSERT INTO users (user_id, country, plan, updated_at)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	now := time.Now()
	for _, u := range users {
		ts := u.UpdatedAt
		if ts.IsZero() {
			ts = now
		}
		if err := batch.Append(u.UserID, u.Country, u.Plan, ts); err != nil {
			return fmt.Errorf("append row: %w", err)
		}
	}
	return batch.Send()
}

// GetUser returns the newest version of one user. FINAL makes ClickHouse
// apply the engine's merge rule at read time, so this is correct even when
// several versions are still in separate parts. It costs extra work for
// every read, which is fine for a point lookup and expensive for a scan.
func (r *Repository) GetUser(ctx context.Context, userID uint64) (*User, error) {
	var u User
	err := r.client.QueryRow(ctx,
		"SELECT user_id, country, plan, updated_at FROM users FINAL WHERE user_id = {user_id:UInt64}",
		cl.Named("user_id", userID),
	).Scan(&u.UserID, &u.Country, &u.Plan, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}
	return &u, nil
}

// ReloadUserLookup forces the users_dict dictionary to reload from the users
// table instead of waiting for its LIFETIME refresh.
func (r *Repository) ReloadUserLookup(ctx context.Context) error {
	return r.client.Exec(ctx, "SYSTEM RELOAD DICTIONARY users_dict")
}

// EventsByCountry counts events in [from, to) per user country. The country
// comes from the users_dict dictionary via dictGet, a hash lookup per row,
// rather than a JOIN. Users unknown to the dictionary count under "".
func (r *Repository) EventsByCountry(ctx context.Context, from, to time.Time) ([]CountryCount, error) {
	rows, err := r.client.Query(ctx, `
		SELECT dictGet('users_dict', 'country', user_id) AS country, count() AS c
		FROM events
		WHERE ts >= {from:DateTime} AND ts < {to:DateTime}
		GROUP BY country ORDER BY c DESC, country`,
		cl.Named("from", from.UTC().Format(time.DateTime)),
		cl.Named("to", to.UTC().Format(time.DateTime)),
	)
	if err != nil {
		return nil, fmt.Errorf("query events by country: %w", err)
	}
	defer rows.Close()

	out := []CountryCount{}
	for rows.Next() {
		var c CountryCount
		if err := rows.Scan(&c.Country, &c.Events); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Funnel uses windowFunnel, which walks each user's events in time order and
// reports how many steps of the chain happened in sequence within window.
// The GROUP BY user_id turns a user's whole history into one row; there is
// no self-join. windowFunnel wants a plain DateTime, hence the cast.
func (r *Repository) Funnel(ctx context.Context, from, to time.Time, window time.Duration) (*Funnel, error) {
	var f Funnel
	err := r.client.QueryRow(ctx, `
		SELECT countIf(level >= 1), countIf(level >= 2), countIf(level >= 3)
		FROM (
			SELECT user_id,
			       windowFunnel({window:UInt32})(toDateTime(ts),
			           event_type = 'view', event_type = 'click', event_type = 'purchase') AS level
			FROM events
			WHERE ts >= {from:DateTime} AND ts < {to:DateTime}
			GROUP BY user_id
		)`,
		cl.Named("window", uint32(window.Seconds())),
		cl.Named("from", from.UTC().Format(time.DateTime)),
		cl.Named("to", to.UTC().Format(time.DateTime)),
	).Scan(&f.Viewed, &f.Clicked, &f.Purchased)
	if err != nil {
		return nil, fmt.Errorf("query funnel: %w", err)
	}
	return &f, nil
}

// Retention uses the retention() aggregate: per user it yields an array of
// flags, one per condition, where flag i is set only if condition 1 (active
// on day) also holds. sumForEach then adds the arrays element-wise across
// users, which is the array style of aggregation ClickHouse favours.
func (r *Repository) Retention(ctx context.Context, day time.Time, days int) (*Retention, error) {
	if days < 1 || days > 30 {
		return nil, fmt.Errorf("days must be 1..30, got %d", days)
	}
	conds := make([]string, 0, days+1)
	for i := 0; i <= days; i++ {
		conds = append(conds, fmt.Sprintf("toDate(ts) = toDate({day:String}) + %d", i))
	}
	res := Retention{Day: day.UTC().Truncate(24 * time.Hour)}
	err := r.client.QueryRow(ctx, fmt.Sprintf(`
		SELECT sumForEach(r)
		FROM (
			SELECT user_id, retention(%s) AS r
			FROM events
			WHERE toDate(ts) BETWEEN toDate({day:String}) AND toDate({day:String}) + {days:UInt8}
			GROUP BY user_id
		)`, strings.Join(conds, ", ")),
		cl.Named("day", res.Day.Format(time.DateOnly)),
		cl.Named("days", uint8(days)),
	).Scan(&res.Days)
	if err != nil {
		return nil, fmt.Errorf("query retention: %w", err)
	}
	return &res, nil
}

// TopUsersByCountry ranks users by event count within their country using a
// window function over an aggregation, and picks each user's most recent page
// with argMax, which returns the value of one column at the maximum of
// another without a second pass.
func (r *Repository) TopUsersByCountry(ctx context.Context, from, to time.Time, n int) ([]TopUser, error) {
	rows, err := r.client.Query(ctx, `
		SELECT country, user_id, events, last_page
		FROM (
			SELECT dictGet('users_dict', 'country', user_id) AS country,
			       user_id,
			       count() AS events,
			       argMax(payload.page, ts) AS last_page,
			       row_number() OVER (PARTITION BY country ORDER BY events DESC, user_id) AS rn
			FROM events
			WHERE ts >= {from:DateTime} AND ts < {to:DateTime}
			GROUP BY country, user_id
		)
		WHERE rn <= {n:UInt32}
		ORDER BY country, rn`,
		cl.Named("from", from.UTC().Format(time.DateTime)),
		cl.Named("to", to.UTC().Format(time.DateTime)),
		cl.Named("n", uint32(n)),
	)
	if err != nil {
		return nil, fmt.Errorf("query top users: %w", err)
	}
	defer rows.Close()

	out := []TopUser{}
	for rows.Next() {
		var u TopUser
		if err := rows.Scan(&u.Country, &u.UserID, &u.Events, &u.LastPage); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
