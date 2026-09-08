package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	cl "github.com/ClickHouse/clickhouse-go/v2"
)

const (
	// MaxOutputBytes caps what one tool result may put into the model's
	// context. max_result_rows (migration 007) caps rows, not width.
	MaxOutputBytes = 32 << 10
)

// runSQL executes one SELECT written by the model. The validation here is
// for fast, readable errors; the fence is the ClickHouse user's grants and
// settings profile, which hold even if this function is bypassed.
type runSQL struct{ *Set }

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

// Result is what the model sees from run_sql. Rows are positional to keep tokens
// down; Columns gives the names.
type Result struct {
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

	var res Result
	ctx = cl.Context(r.tagged(ctx),
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
func encode(res Result) (string, error) {
	for {
		b, err := json.Marshal(res)
		if err != nil {
			return "", err
		}
		if len(b) <= MaxOutputBytes || len(res.Rows) == 0 {
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
