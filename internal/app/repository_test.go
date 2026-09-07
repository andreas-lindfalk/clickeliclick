package app

import (
	"context"
	"encoding/json"
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

// recentMinute is a fixed point an hour ago. Test rows must be younger than
// the 90 day TTL on events (migration 005) or they are dropped on insert.
func recentMinute() time.Time {
	return time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
}

func TestInsertAndRecentEvents(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))

	ctx := context.Background()

	base := recentMinute()
	in := []Event{
		{TS: base, UserID: 1, EventType: "click", Payload: json.RawMessage(`{"a":1}`)},
		{TS: base.Add(time.Second), UserID: 2, EventType: "view", Payload: json.RawMessage(`{}`)},
		{TS: base.Add(2 * time.Second), UserID: 1, EventType: "click", Payload: json.RawMessage(`{"a":2}`)},
	}
	require.NoError(t, repo.InsertEvents(ctx, in))

	got, err := repo.RecentEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 3)

	// RecentEvents orders by ts DESC. The typed paths page and ref (migration
	// 004) exist on every document, defaulting to "", even when never written.
	require.JSONEq(t, `{"a":2,"page":"","ref":""}`, string(got[0].Payload))
	require.JSONEq(t, `{"page":"","ref":""}`, string(got[1].Payload))
	require.JSONEq(t, `{"a":1,"page":"","ref":""}`, string(got[2].Payload))
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

	base := recentMinute()
	require.NoError(t, repo.InsertEvents(ctx, []Event{
		{TS: base, UserID: 1, EventType: "view", Payload: json.RawMessage(`{"n":"first"}`)},
		{TS: base.Add(time.Second), UserID: 2, EventType: "view", Payload: json.RawMessage(`{"n":"other user"}`)},
		{TS: base.Add(2 * time.Second), UserID: 1, EventType: "click", Payload: json.RawMessage(`{"n":"second"}`)},
	}))

	got, err := repo.RecentEventsByUser(ctx, 1, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.JSONEq(t, `{"n":"second","page":"","ref":""}`, string(got[0].Payload))
	require.JSONEq(t, `{"n":"first","page":"","ref":""}`, string(got[1].Payload))
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
	base := recentMinute()
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

	require.NoError(t, repo.InsertEventAsync(ctx, Event{UserID: 7, EventType: "click", Payload: json.RawMessage(`{"async":true}`)}))

	// wait=true means the async buffer has been flushed, so the row is visible.
	got, err := repo.RecentEvents(ctx, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, uint64(7), got[0].UserID)
	require.JSONEq(t, `{"async":true,"page":"","ref":""}`, string(got[0].Payload))
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

	m0 := recentMinute()
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

	m0 := recentMinute()
	require.NoError(t, repo.InsertEvents(ctx, []Event{{TS: m0, UserID: 1, EventType: "view"}}))
	require.NoError(t, repo.InsertEvents(ctx, []Event{{TS: m0, UserID: 1, EventType: "view"}, {TS: m0, UserID: 2, EventType: "view"}}))

	got, err := repo.StatsPerMinute(ctx, m0, m0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []MinuteStats{{Minute: m0, EventType: "view", Events: 3, Users: 2}}, got)
}

// A nested document with keys the schema has never seen round-trips intact:
// the typed paths (page, ref) and the dynamic ones are reassembled on read.
// Key order is not preserved, JSONEq does not care.
func TestPayloadRoundTrip(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))
	ctx := context.Background()

	doc := `{"page":"/checkout","ref":"google","experiment":{"name":"blue-button","variant":2},"tags":["a","b"],"price":9.5}`
	require.NoError(t, repo.InsertEvents(ctx, []Event{{UserID: 1, EventType: "view", Payload: json.RawMessage(doc)}}))

	got, err := repo.RecentEvents(ctx, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.JSONEq(t, doc, string(got[0].Payload))
}

func TestTopPagesByRef(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))
	ctx := context.Background()

	ev := func(ref, page string) Event {
		return Event{UserID: 1, EventType: "view", Payload: json.RawMessage(`{"page":"` + page + `","ref":"` + ref + `"}`)}
	}
	require.NoError(t, repo.InsertEvents(ctx, []Event{
		ev("google", "/a"), ev("google", "/a"), ev("google", "/b"),
		ev("direct", "/a"), ev("direct", "/c"),
		{UserID: 2, EventType: "view"}, // no payload at all
	}))

	got, err := repo.TopPagesByRef(ctx, "google", 10)
	require.NoError(t, err)
	require.Equal(t, []PageCount{{Page: "/a", Count: 2}, {Page: "/b", Count: 1}}, got)
}

func TestDeleteUserEvents(t *testing.T) {
	client := testServer.NewClient(t)
	repo := NewRepository(client)
	ctx := context.Background()

	m0 := recentMinute()
	require.NoError(t, repo.InsertEvents(ctx, []Event{
		{TS: m0, UserID: 1, EventType: "view"},
		{TS: m0, UserID: 1, EventType: "click"},
		{TS: m0, UserID: 2, EventType: "view"},
	}))

	require.NoError(t, repo.DeleteUserEvents(ctx, 1))

	// Gone from every read path, including the projection-backed one.
	got, err := repo.RecentEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, uint64(2), got[0].UserID)
	byUser, err := repo.RecentEventsByUser(ctx, 1, 10)
	require.NoError(t, err)
	require.Empty(t, byUser)

	// Physically gone, not masked: the parts were rewritten.
	var remaining uint64
	require.NoError(t, client.QueryRow(ctx,
		"SELECT count() FROM events WHERE user_id = 1 SETTINGS apply_deleted_mask = 0, optimize_use_projections = 0").Scan(&remaining))
	require.Zero(t, remaining)

	// The rollup was fed by the inserts and knows nothing about the delete.
	stats, err := repo.StatsPerMinute(ctx, m0, m0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []MinuteStats{
		{Minute: m0, EventType: "click", Events: 1, Users: 1},
		{Minute: m0, EventType: "view", Events: 2, Users: 2},
	}, stats)
}

// TTL is enforced whenever a part is written or merged. A row that is already
// expired at insert time never lands at all.
func TestTTLDropsExpiredRowsOnInsert(t *testing.T) {
	repo := NewRepository(testServer.NewClient(t))
	ctx := context.Background()

	require.NoError(t, repo.InsertEvents(ctx, []Event{
		{TS: time.Now().Add(-100 * 24 * time.Hour), UserID: 1, EventType: "view"},
		{TS: time.Now(), UserID: 2, EventType: "view"},
	}))

	got, err := repo.RecentEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, uint64(2), got[0].UserID)
}
