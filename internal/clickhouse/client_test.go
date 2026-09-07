package clickhouse_test

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"

	"clickeliclick/internal/clickhouse"
)

const (
	image    = "clickhouse/clickhouse-server:latest"
	database = "poc"
	user     = "default"
	password = "test"
)

// addr is the host:port of the shared ClickHouse container started in TestMain.
var addr string

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ctr, err := tcclickhouse.Run(ctx, image,
		tcclickhouse.WithDatabase(database),
		tcclickhouse.WithUsername(user),
		tcclickhouse.WithPassword(password),
		tcclickhouse.WithInitScripts(filepath.Join("..", "..", "migrations", "001_events.sql")),
	)
	if err != nil {
		log.Fatalf("start clickhouse container: %v", err)
	}

	addr, err = ctr.ConnectionHost(ctx)
	if err != nil {
		log.Fatalf("connection host: %v", err)
	}

	code := m.Run()

	if err := testcontainers.TerminateContainer(ctr); err != nil {
		log.Printf("terminate container: %v", err)
	}
	os.Exit(code)
}

// newClient connects to the shared container and truncates the events table
// so each test starts from a clean slate.
func newClient(t *testing.T) *clickhouse.Client {
	t.Helper()
	ctx := context.Background()

	c, err := clickhouse.New(ctx, addr, database, user, password)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })

	require.NoError(t, c.Exec(ctx, "TRUNCATE TABLE events"))
	return c
}

func TestInsertAndRecentEvents(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	in := []clickhouse.Event{
		{TS: base, UserID: 1, EventType: "click", Payload: `{"a":1}`},
		{TS: base.Add(time.Second), UserID: 2, EventType: "view", Payload: `{}`},
		{TS: base.Add(2 * time.Second), UserID: 1, EventType: "click", Payload: `{"a":2}`},
	}
	require.NoError(t, c.InsertEvents(ctx, in))

	got, err := c.RecentEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 3)

	// RecentEvents orders by ts DESC.
	require.Equal(t, in[2].Payload, got[0].Payload)
	require.Equal(t, in[1].Payload, got[1].Payload)
	require.Equal(t, in[0].Payload, got[2].Payload)
	require.True(t, got[0].TS.Equal(in[2].TS), "ts round-trips through DateTime64(3)")
}

func TestRecentEventsRespectsLimit(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	var in []clickhouse.Event
	for i := range 20 {
		in = append(in, clickhouse.Event{UserID: uint64(i), EventType: "click"})
	}
	require.NoError(t, c.InsertEvents(ctx, in))

	got, err := c.RecentEvents(ctx, 5)
	require.NoError(t, err)
	require.Len(t, got, 5)
}

func TestInsertDefaultsTimestamp(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	require.NoError(t, c.InsertEvents(ctx, []clickhouse.Event{{UserID: 1, EventType: "view"}}))

	got, err := c.RecentEvents(ctx, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, got[0].TS.After(before), "zero TS should be filled with now()")
}

func TestCountByType(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	require.NoError(t, c.InsertEvents(ctx, []clickhouse.Event{
		{UserID: 1, EventType: "click"},
		{UserID: 2, EventType: "click"},
		{UserID: 3, EventType: "view"},
	}))

	counts, err := c.CountByType(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]uint64{"click": 2, "view": 1}, counts)
}

func TestCountByTypeEmpty(t *testing.T) {
	c := newClient(t)

	counts, err := c.CountByType(context.Background())
	require.NoError(t, err)
	require.Empty(t, counts)
}
