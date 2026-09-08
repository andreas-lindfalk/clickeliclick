package tools_test

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"testing"
	"time"

	cl "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/require"

	"clickeliclick/internal/agent"
	"clickeliclick/internal/agent/tools"
	"clickeliclick/internal/pkg/clickhouse"
	"clickeliclick/internal/pkg/clickhouse/clickhousetest"
)

// One ClickHouse container for the package. The loop tests (agent_test.go,
// chat_test.go) do not use it.

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

func newTools(t *testing.T) (admin *clickhouse.Client, set *tools.Set) {
	t.Helper()
	a, agent := newAgentClient(t)
	return a, tools.New(agent, "conv-"+t.Name())
}

func findTool(t *testing.T, set *tools.Set, name string) agent.Tool {
	t.Helper()
	for _, tool := range set.All() {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("no tool %q", name)
	return nil
}

func runSQLTool(t *testing.T, set *tools.Set, sql string) (tools.Result, error) {
	t.Helper()
	in, err := json.Marshal(map[string]string{"sql": sql})
	require.NoError(t, err)
	out, err := findTool(t, set, "run_sql").Call(context.Background(), in)
	if err != nil {
		return tools.Result{}, err
	}
	var res tools.Result
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	return res, nil
}

func callTool(t *testing.T, set *tools.Set, name string, input any) (string, error) {
	t.Helper()
	in, err := json.Marshal(input)
	require.NoError(t, err)
	return findTool(t, set, name).Call(context.Background(), in)
}
