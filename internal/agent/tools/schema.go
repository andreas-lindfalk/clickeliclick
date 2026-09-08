package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// describeSchema tells the model what it can query. It reads system.tables
// and system.columns as the agent user, so it lists exactly what the grants
// allow, and adds what those tables cannot express: the dictionary (dictGet
// does not imply SHOW), the row policy and the result cap.
type describeSchema struct{ *Set }

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
