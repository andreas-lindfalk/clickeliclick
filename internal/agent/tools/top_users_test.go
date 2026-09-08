package tools_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	cl "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/require"

	"clickeliclick/internal/app"
)

func TestTopUsersToolRunsAsAgentWithTag(t *testing.T) {
	admin, set := newTools(t)
	repo := app.NewRepository(admin)
	ctx := context.Background()

	require.NoError(t, repo.UpsertUsers(ctx, []app.User{{UserID: 1, Country: "SE"}, {UserID: 2, Country: "SE"}}))
	require.NoError(t, repo.ReloadUserLookup(ctx))
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	require.NoError(t, repo.InsertEvents(ctx, []app.Event{
		{TS: t0, UserID: 1, EventType: "view", Payload: json.RawMessage(`{"page":"/a"}`)},
		{TS: t0.Add(time.Second), UserID: 1, EventType: "view", Payload: json.RawMessage(`{"page":"/b"}`)},
		{TS: t0, UserID: 2, EventType: "view", Payload: json.RawMessage(`{"page":"/x"}`)},
	}))

	out, err := callTool(t, set, "top_users", map[string]any{"n": 1})
	require.NoError(t, err)
	require.JSONEq(t, `[{"country":"SE","user_id":1,"events":2,"last_page":"/b"}]`, out)

	// The curated query went through the restricted user and carries the
	// conversation id, same as run_sql.
	require.NoError(t, admin.Exec(ctx, "SYSTEM FLUSH LOGS"))
	var n uint64
	require.NoError(t, admin.QueryRow(ctx,
		`SELECT count() FROM system.query_log WHERE user = 'agent' AND type = 'QueryFinish' AND log_comment = {c:String} AND query LIKE '%row_number%'`,
		cl.Named("c", set.ConversationID())).Scan(&n))
	require.Equal(t, uint64(1), n)
}
