package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	cl "github.com/ClickHouse/clickhouse-go/v2"

	"clickeliclick/internal/pkg/clickhouse"
)

const (
	// maxOutputBytes caps what one tool result may put into the model's
	// context. max_result_rows (migration 007) caps rows, not width.
	maxOutputBytes = 32 << 10
)

// Tools is the set the model gets: bound to one connection, which must be
// authenticated as the restricted user, and one conversation id that tags
// every query in system.query_log.
type Tools struct {
	client         *clickhouse.Client
	conversationID string
}

func NewTools(client *clickhouse.Client, conversationID string) *Tools {
	return &Tools{client: client, conversationID: conversationID}
}

// All returns the tools in the order they are offered to the model.
func (t *Tools) All() []Tool {
	return []Tool{describeSchema{t}, runSQL{t}}
}

// describeSchema tells the model what it can query. It reads system.tables
// and system.columns as the agent user, so it lists exactly what the grants
// allow, and adds what those tables cannot express: the dictionary (dictGet
// does not imply SHOW), the row policy and the result cap.
type describeSchema struct{ *Tools }

func (describeSchema) Name() string { return "describe_schema" }
func (describeSchema) Description() string {
	return "Lists the ClickHouse tables and columns available to run_sql, with their engines and sort keys. Call this before writing SQL."
}
func (describeSchema) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

const schemaNotes = `Database: poc, ClickHouse SQL dialect. Only one SELECT per run_sql call.
Rules enforced by the server: rows in events and events_per_minute are limited to the last 30 days;
a result over 200 rows fails, so aggregate or add LIMIT; queries over 5 seconds are killed.
Columns of type AggregateFunction(f, ...) are partial states and must be read with fMerge(col),
e.g. countMerge(events), uniqMerge(users).
Timestamps are UTC. windowFunnel and retention need DateTime, so cast: toDateTime(ts).
Dictionary users_dict maps user_id to country and plan: dictGet('users_dict', 'country', user_id).
`

func (d describeSchema) Call(ctx context.Context, _ json.RawMessage) (string, error) {
	rows, err := d.client.Query(ctx, `
		SELECT t.name, t.engine, t.sorting_key, groupArray((c.name, c.type))
		FROM system.tables AS t
		JOIN system.columns AS c ON c.database = t.database AND c.table = t.name
		WHERE t.database = currentDatabase()
		GROUP BY t.name, t.engine, t.sorting_key
		ORDER BY t.name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var b strings.Builder
	b.WriteString(schemaNotes)
	for rows.Next() {
		var name, engine, sortKey string
		var cols [][]any
		if err := rows.Scan(&name, &engine, &sortKey, &cols); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\nTABLE %s (%s, ORDER BY (%s))\n", name, engine, sortKey)
		for _, c := range cols {
			fmt.Fprintf(&b, "  %s %s\n", c[0], c[1])
		}
	}
	return b.String(), rows.Err()
}

// runSQL executes one SELECT written by the model. The validation here is
// for fast, readable errors; the fence is the ClickHouse user's grants and
// settings profile, which hold even if this function is bypassed.
type runSQL struct{ *Tools }

func (runSQL) Name() string { return "run_sql" }
func (runSQL) Description() string {
	return "Runs one read-only SQL SELECT against ClickHouse and returns the result as JSON: column names, rows, and how many rows the query read. Errors from ClickHouse are returned verbatim; fix the query and retry."
}
func (runSQL) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sql": map[string]any{"type": "string", "description": "One SELECT statement in ClickHouse SQL."},
		},
		"required": []string{"sql"},
	}
}

// queryResult is what the model sees. Rows are positional to keep tokens
// down; Columns gives the names.
type queryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowsRead  uint64   `json:"rows_read"`
	BytesRead uint64   `json:"bytes_read"`
	ElapsedMS int64    `json:"elapsed_ms"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (r runSQL) Call(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	sql, err := validateSQL(in.SQL)
	if err != nil {
		return "", err
	}

	var res queryResult
	ctx = cl.Context(noDeadline{ctx},
		cl.WithSettings(cl.Settings{"log_comment": r.conversationID}),
		cl.WithProgress(func(p *cl.Progress) {
			res.RowsRead += p.Rows
			res.BytesRead += p.Bytes
		}),
	)

	start := time.Now()
	rows, err := r.client.Query(ctx, sql)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	types := rows.ColumnTypes()
	res.Columns = make([]string, len(types))
	for i, t := range types {
		res.Columns[i] = t.Name()
	}
	res.Rows = [][]any{}
	for rows.Next() {
		dest := make([]any, len(types))
		for i, t := range types {
			if isJSON(t) {
				// The connection settings make the server send JSON as
				// text; the driver's ScanType does not know that.
				dest[i] = new(string)
			} else {
				dest[i] = reflect.New(t.ScanType()).Interface()
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return "", err
		}
		row := make([]any, len(types))
		for i, d := range dest {
			row[i] = jsonValue(types[i], reflect.ValueOf(d).Elem().Interface())
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		// This is where max_result_rows and max_execution_time surface.
		return "", err
	}
	res.ElapsedMS = time.Since(start).Milliseconds()

	return encode(res)
}

// noDeadline hides the context deadline from the driver but keeps its
// cancellation. clickhouse-go turns a deadline into a max_execution_time
// query setting, which the readonly profile rejects. The server's own
// max_execution_time is the timeout here; the caller's deadline still
// cancels the wait on our side.
type noDeadline struct{ context.Context }

func (noDeadline) Deadline() (time.Time, bool) { return time.Time{}, false }

func isJSON(t interface{ DatabaseTypeName() string }) bool {
	return strings.HasPrefix(t.DatabaseTypeName(), "JSON")
}

// jsonValue makes a scanned value marshal sensibly: JSON text is embedded
// as JSON rather than as a quoted string.
func jsonValue(t interface{ DatabaseTypeName() string }, v any) any {
	if s, ok := v.(string); ok && isJSON(t) {
		return json.RawMessage(s)
	}
	return v
}

// encode marshals the result, dropping rows from the end until it fits the
// output cap. The model is told when that happened.
func encode(res queryResult) (string, error) {
	for {
		b, err := json.Marshal(res)
		if err != nil {
			return "", err
		}
		if len(b) <= maxOutputBytes || len(res.Rows) == 0 {
			return string(b), nil
		}
		res.Rows = res.Rows[:len(res.Rows)/2]
		res.Truncated = true
	}
}

var errNotSelect = errors.New("only a single SELECT (or WITH ... SELECT, or EXPLAIN) is allowed")

// validateSQL accepts one statement that starts like a read. A semicolon
// inside a string literal is rejected too, which is a limitation we accept
// for clear errors and no SQL parser.
func validateSQL(sql string) (string, error) {
	sql = strings.TrimSpace(sql)
	sql = strings.TrimSpace(strings.TrimSuffix(sql, ";"))
	if sql == "" {
		return "", errors.New("empty query")
	}
	if strings.Contains(sql, ";") {
		return "", errors.New("one statement per call")
	}
	first := strings.ToUpper(strings.Fields(sql)[0])
	switch first {
	case "SELECT", "WITH", "EXPLAIN":
		return sql, nil
	}
	return "", fmt.Errorf("%w, got %s", errNotSelect, first)
}
