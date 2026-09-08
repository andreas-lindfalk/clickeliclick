package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cl "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/require"

	"clickeliclick/internal/agent/tools"
)

func TestRunSQL(t *testing.T) {
	admin, set := newTools(t)
	ctx := context.Background()

	ts := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, admin.Exec(ctx,
		`INSERT INTO events (ts, user_id, event_type, payload) VALUES ({ts:DateTime64(3)}, 7, 'click', '{"page":"/a"}'), ({ts:DateTime64(3)}, 8, 'view', '{}')`,
		cl.Named("ts", ts.Format("2006-01-02 15:04:05.000"))))

	res, err := runSQLTool(t, set, "SELECT user_id, event_type, ts, payload FROM events ORDER BY user_id;")
	require.NoError(t, err)

	require.Equal(t, []string{"user_id", "event_type", "ts", "payload"}, res.Columns)
	require.Len(t, res.Rows, 2)
	require.Equal(t, float64(7), res.Rows[0][0]) // JSON numbers decode as float64
	require.Equal(t, "click", res.Rows[0][1])
	require.Equal(t, ts.Format(time.RFC3339), res.Rows[0][2])
	// The JSON column is embedded as an object, not as a quoted string.
	require.Equal(t, map[string]any{"page": "/a", "ref": ""}, res.Rows[0][3])
	require.GreaterOrEqual(t, res.RowsRead, uint64(2))
	require.False(t, res.Truncated)
}

func TestRunSQLRejectsNonSelect(t *testing.T) {
	_, set := newTools(t)

	for _, sql := range []string{
		"",
		"INSERT INTO events (user_id) VALUES (1)",
		"ALTER TABLE events DELETE WHERE 1",
		"SELECT 1; SELECT 2",
		"DROP TABLE events",
	} {
		_, err := runSQLTool(t, set, sql)
		require.Error(t, err, sql)
	}
}

func TestRunSQLReturnsClickHouseErrors(t *testing.T) {
	admin, set := newTools(t)
	insertEvents(t, admin, time.Now().UTC().Add(-time.Hour), 300)

	// Over the result cap: the model gets ClickHouse's message.
	_, err := runSQLTool(t, set, "SELECT user_id FROM events")
	require.ErrorContains(t, err, "Limit for result exceeded")

	// Trying to lift a cap.
	_, err = runSQLTool(t, set, "SELECT count() FROM events SETTINGS max_result_rows = 100000")
	require.ErrorContains(t, err, "readonly")

	// A plain mistake.
	_, err = runSQLTool(t, set, "SELECT nope FROM events")
	require.ErrorContains(t, err, "nope")
}

func TestRunSQLIsTaggedWithConversation(t *testing.T) {
	admin, set := newTools(t)
	ctx := context.Background()

	_, err := runSQLTool(t, set, "SELECT count() FROM events")
	require.NoError(t, err)

	require.NoError(t, admin.Exec(ctx, "SYSTEM FLUSH LOGS"))
	var n uint64
	require.NoError(t, admin.QueryRow(ctx,
		`SELECT count() FROM system.query_log WHERE user = 'agent' AND type = 'QueryFinish' AND log_comment = {c:String}`,
		cl.Named("c", set.ConversationID())).Scan(&n))
	require.Equal(t, uint64(1), n)
}

func TestRunSQLTruncatesWideResults(t *testing.T) {
	admin, set := newTools(t)
	insertEvents(t, admin, time.Now().UTC().Add(-time.Hour), 200)

	// 200 rows of 1 KiB each is over the output cap.
	res, err := runSQLTool(t, set, "SELECT repeat('x', 1024) AS wide FROM events")
	require.NoError(t, err)
	require.True(t, res.Truncated)
	require.Less(t, len(res.Rows), 200)
	require.NotEmpty(t, res.Rows)

	out, err := json.Marshal(res)
	require.NoError(t, err)
	require.LessOrEqual(t, len(out), tools.MaxOutputBytes)
	require.True(t, strings.HasPrefix(res.Rows[0][0].(string), "xxx"))
}
