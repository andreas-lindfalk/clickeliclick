package agent

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	cl "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/require"

	"clickeliclick/internal/pkg/clickhouse"
	"clickeliclick/internal/pkg/clickhouse/clickhousetest"
)

// These tests exercise migration 007 from the agent user's side: every fence
// the LLM is supposed to run into, hit on purpose.

var testServer *clickhousetest.Server

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var err error
	testServer, err = clickhousetest.Start(ctx)
	if err != nil {
		log.Fatalf("start clickhouse: %v", err)
	}

	code := m.Run()

	if err := testServer.Close(); err != nil {
		log.Printf("terminate clickhouse: %v", err)
	}
	os.Exit(code)
}

// ClickHouse error codes we expect to hit. The driver surfaces them as
// *clickhouse.Exception.
const (
	codeReadonly     = 164
	codeTooManyRows  = 396
	codeAccessDenied = 497
)

func requireCode(t *testing.T, err error, code int32) {
	t.Helper()
	var e *cl.Exception
	require.ErrorAs(t, err, &e)
	require.Equal(t, code, e.Code, "unexpected ClickHouse error: %v", err)
}

// newAgentClient connects as the restricted user. It goes through
// NewClient first so the tables are truncated as in every other test.
func newAgentClient(t *testing.T) (admin, agent *clickhouse.Client) {
	t.Helper()
	admin = testServer.NewClient(t)

	cfg := testServer.Config()
	cfg.User, cfg.Password = "agent", "agent"
	agent, err := clickhouse.New(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, agent.Close()) })
	return admin, agent
}

func insertEvents(t *testing.T, c *clickhouse.Client, ts time.Time, n int) {
	t.Helper()
	require.NoError(t, c.Exec(context.Background(),
		`INSERT INTO events (ts, user_id, event_type) SELECT {ts:DateTime64(3)}, number, 'view' FROM numbers({n:UInt64})`,
		cl.Named("ts", ts.UTC().Format("2006-01-02 15:04:05.000")), cl.Named("n", uint64(n))))
}

func TestAgentCannotWrite(t *testing.T) {
	_, agent := newAgentClient(t)
	ctx := context.Background()

	requireCode(t, agent.Exec(ctx, `INSERT INTO events (user_id, event_type) VALUES (1, 'x')`), codeAccessDenied)
	requireCode(t, agent.Exec(ctx, `ALTER TABLE events DELETE WHERE 1`), codeAccessDenied)
	requireCode(t, agent.Exec(ctx, `CREATE TABLE scratch (x UInt8) ENGINE = Memory`), codeAccessDenied)
	requireCode(t, agent.Exec(ctx, `TRUNCATE TABLE events`), codeAccessDenied)
}

func TestAgentCannotChangeSettings(t *testing.T) {
	admin, agent := newAgentClient(t)
	ctx := context.Background()

	// readonly = 1 freezes the profile: no lifting the caps per query.
	requireCode(t, agent.Exec(ctx, `SELECT 1 SETTINGS max_execution_time = 100`), codeReadonly)
	requireCode(t, agent.Exec(ctx, `SELECT 1 SETTINGS max_result_rows = 1000000`), codeReadonly)
	requireCode(t, agent.Exec(ctx, `SELECT 1 SETTINGS readonly = 0`), codeReadonly)

	// log_comment is whitelisted, and the admin can find the query by it.
	require.NoError(t, agent.Exec(ctx, `SELECT 1 SETTINGS log_comment = 'conversation-42'`))
	require.NoError(t, admin.Exec(ctx, `SYSTEM FLUSH LOGS`))
	var n uint64
	require.NoError(t, admin.QueryRow(ctx,
		`SELECT count() FROM system.query_log WHERE user = 'agent' AND log_comment = 'conversation-42' AND type = 'QueryFinish'`).Scan(&n))
	require.Equal(t, uint64(1), n)
}

func TestAgentSeesOnlyRecentRows(t *testing.T) {
	admin, agent := newAgentClient(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Minute)
	insertEvents(t, admin, now.Add(-time.Hour), 3)
	insertEvents(t, admin, now.Add(-40*24*time.Hour), 2) // inside the 90 day TTL, outside the policy

	count := func(c *clickhouse.Client, q string) uint64 {
		var n uint64
		require.NoError(t, c.QueryRow(ctx, q).Scan(&n))
		return n
	}
	require.Equal(t, uint64(5), count(admin, `SELECT count() FROM events`))
	require.Equal(t, uint64(3), count(agent, `SELECT count() FROM events`))

	// The by_user projection is read for user-first queries; the policy
	// still applies.
	require.Equal(t, uint64(1), count(agent, `SELECT count() FROM events WHERE user_id = 0`))

	// The rollup has its own policy; without it, older days would leak as
	// aggregates.
	require.Equal(t, uint64(5), count(admin, `SELECT countMerge(events) FROM events_per_minute`))
	require.Equal(t, uint64(3), count(agent, `SELECT countMerge(events) FROM events_per_minute`))
}

func TestAgentCannotReachOutsideItsTables(t *testing.T) {
	_, agent := newAgentClient(t)
	ctx := context.Background()

	requireCode(t, agent.Exec(ctx, `SELECT count() FROM system.query_log`), codeAccessDenied)
	requireCode(t, agent.Exec(ctx, `SELECT * FROM url('http://example.com', 'RawBLOB')`), codeAccessDenied)
	requireCode(t, agent.Exec(ctx, `SELECT * FROM remote('localhost', 'system', 'users')`), codeAccessDenied)

	// system.tables is readable but filtered to what the agent has SELECT
	// on. The dictionary is absent: dictGet does not imply SHOW, so the
	// model will have to be told about it. This is what a describe_schema
	// tool will show.
	rows, err := agent.Query(ctx, `SELECT name FROM system.tables WHERE database = currentDatabase() ORDER BY name`)
	require.NoError(t, err)
	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"events", "events_per_minute", "users"}, names)

	// The dictionary works through dictGet, which has its own grant.
	var country string
	require.NoError(t, agent.QueryRow(ctx, `SELECT dictGet('users_dict', 'country', toUInt64(1))`).Scan(&country))
}

func TestAgentResultIsCapped(t *testing.T) {
	admin, agent := newAgentClient(t)
	ctx := context.Background()
	insertEvents(t, admin, time.Now().UTC().Add(-time.Hour), 300)

	// Over max_result_rows the query throws rather than truncating, so the
	// model learns to aggregate or LIMIT instead of reasoning from a
	// partial result.
	rows, err := agent.Query(ctx, `SELECT user_id FROM events`)
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
	}
	requireCode(t, err, codeTooManyRows)

	rows, err = agent.Query(ctx, `SELECT user_id FROM events LIMIT 100`)
	require.NoError(t, err)
	n := 0
	for rows.Next() {
		n++
	}
	require.NoError(t, rows.Close())
	require.Equal(t, 100, n)

	var total uint64
	require.NoError(t, agent.QueryRow(ctx, `SELECT count() FROM events`).Scan(&total))
	require.Equal(t, uint64(300), total)
}

func TestAgentHasQuota(t *testing.T) {
	_, agent := newAgentClient(t)

	var maxQueries *uint64
	require.NoError(t, agent.QueryRow(context.Background(),
		`SELECT max_queries FROM system.quota_usage`).Scan(&maxQueries))
	require.NotNil(t, maxQueries)
	require.Equal(t, uint64(200), *maxQueries)
}
