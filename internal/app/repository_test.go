package app

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"clickeliclick/internal/pkg/clickhouse/clickhousetest"

	"github.com/stretchr/testify/require"
)

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

	// os.Exit skips deferred calls, so terminate explicitly.
	if err := testServer.Close(); err != nil {
		log.Printf("terminate clickhouse: %v", err)
	}
	os.Exit(code)
}

func TestInsertAndRecentEvents(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))

	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	in := []Event{
		{TS: base, UserID: 1, EventType: "click", Payload: `{"a":1}`},
		{TS: base.Add(time.Second), UserID: 2, EventType: "view", Payload: `{}`},
		{TS: base.Add(2 * time.Second), UserID: 1, EventType: "click", Payload: `{"a":2}`},
	}
	require.NoError(t, repo.InsertEvents(ctx, in))

	got, err := repo.RecentEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 3)

	// RecentEvents orders by ts DESC.
	require.Equal(t, in[2].Payload, got[0].Payload)
	require.Equal(t, in[1].Payload, got[1].Payload)
	require.Equal(t, in[0].Payload, got[2].Payload)
	require.True(t, got[0].TS.Equal(in[2].TS), "ts round-trips through DateTime64(3)")
}

func TestRecentEventsRespectsLimit(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))

	ctx := context.Background()

	var in []Event
	for i := range 20 {
		in = append(in, Event{UserID: uint64(i), EventType: "click"})
	}
	require.NoError(t, repo.InsertEvents(ctx, in))

	got, err := repo.RecentEvents(ctx, 5)
	require.NoError(t, err)
	require.Len(t, got, 5)
}

func TestInsertDefaultsTimestamp(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))

	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	require.NoError(t, repo.InsertEvents(ctx, []Event{{UserID: 1, EventType: "view"}}))

	got, err := repo.RecentEvents(ctx, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, got[0].TS.After(before), "zero TS should be filled with now()")
}

func TestRecentEventsByUser(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repo.InsertEvents(ctx, []Event{
		{TS: base, UserID: 1, EventType: "view", Payload: "first"},
		{TS: base.Add(time.Second), UserID: 2, EventType: "view", Payload: "other user"},
		{TS: base.Add(2 * time.Second), UserID: 1, EventType: "click", Payload: "second"},
	}))

	got, err := repo.RecentEventsByUser(ctx, 1, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "second", got[0].Payload)
	require.Equal(t, "first", got[1].Payload)
	for _, e := range got {
		require.Equal(t, uint64(1), e.UserID)
	}
}

func TestInsertEventColumns(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))
	ctx := context.Background()

	const n = 10_000
	cols := EventColumns{
		TS:        make([]time.Time, n),
		UserID:    make([]uint64, n),
		EventType: make([]string, n),
		Payload:   make([]string, n),
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		cols.TS[i] = base.Add(time.Duration(i) * time.Millisecond)
		cols.UserID[i] = uint64(i % 100)
		cols.EventType[i] = "view"
		cols.Payload[i] = "{}"
	}
	require.NoError(t, repo.InsertEventColumns(ctx, cols))

	counts, err := repo.CountByType(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]uint64{"view": n}, counts)
}

func TestInsertEventAsync(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))
	ctx := context.Background()

	require.NoError(t, repo.InsertEventAsync(ctx, Event{UserID: 7, EventType: "click", Payload: `{"async":true}`}))

	// wait=true means the async buffer has been flushed, so the row is visible.
	got, err := repo.RecentEvents(ctx, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, uint64(7), got[0].UserID)
	require.Equal(t, `{"async":true}`, got[0].Payload)
}

func TestCountByType(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))

	ctx := context.Background()

	require.NoError(t, repo.InsertEvents(ctx, []Event{
		{UserID: 1, EventType: "click"},
		{UserID: 2, EventType: "click"},
		{UserID: 3, EventType: "view"},
	}))

	counts, err := repo.CountByType(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]uint64{"click": 2, "view": 1}, counts)
}

func TestCountByTypeEmpty(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))

	counts, err := repo.CountByType(context.Background())
	require.NoError(t, err)
	require.Empty(t, counts)
}

func TestStatsPerMinute(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))
	ctx := context.Background()

	m0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m1 := m0.Add(time.Minute)
	require.NoError(t, repo.InsertEvents(ctx, []Event{
		{TS: m0, UserID: 1, EventType: "view"},
		{TS: m0.Add(10 * time.Second), UserID: 1, EventType: "view"}, // same user again
		{TS: m0.Add(20 * time.Second), UserID: 2, EventType: "view"},
		{TS: m0.Add(30 * time.Second), UserID: 2, EventType: "click"},
		{TS: m1, UserID: 3, EventType: "view"},
	}))

	got, err := repo.StatsPerMinute(ctx, m0, m1.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []MinuteStats{
		{Minute: m0, EventType: "click", Events: 1, Users: 1},
		{Minute: m0, EventType: "view", Events: 3, Users: 2},
		{Minute: m1, EventType: "view", Events: 1, Users: 1},
	}, got)
}

// Two separate inserts for the same minute produce two rows of partial state
// in the rollup. countMerge/uniqMerge with GROUP BY combine them correctly
// whether or not a background merge has happened yet.
func TestStatsPerMinuteMergesAcrossInserts(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))
	ctx := context.Background()

	m0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repo.InsertEvents(ctx, []Event{{TS: m0, UserID: 1, EventType: "view"}}))
	require.NoError(t, repo.InsertEvents(ctx, []Event{{TS: m0, UserID: 1, EventType: "view"}, {TS: m0, UserID: 2, EventType: "view"}}))

	got, err := repo.StatsPerMinute(ctx, m0, m0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []MinuteStats{{Minute: m0, EventType: "view", Events: 3, Users: 2}}, got)
}
