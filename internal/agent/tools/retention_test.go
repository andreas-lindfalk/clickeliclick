package tools_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"clickeliclick/internal/app"
)

func TestRetentionTool(t *testing.T) {
	admin, set := newTools(t)
	repo := app.NewRepository(admin)
	ctx := context.Background()

	day := time.Now().UTC().Add(-5 * 24 * time.Hour).Truncate(24 * time.Hour)
	require.NoError(t, repo.InsertEvents(ctx, []app.Event{
		{TS: day.Add(time.Hour), UserID: 1, EventType: "view"},
		{TS: day.Add(time.Hour), UserID: 2, EventType: "view"},
		{TS: day.Add(25 * time.Hour), UserID: 1, EventType: "view"},
		{TS: day.Add(49 * time.Hour), UserID: 1, EventType: "view"},
		{TS: day.Add(49 * time.Hour), UserID: 2, EventType: "view"},
	}))

	out, err := callTool(t, set, "retention", map[string]any{"day": day.Format(time.DateOnly), "days": 2})
	require.NoError(t, err)
	var res app.Retention
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Equal(t, []uint64{2, 1, 2}, res.Days)

	_, err = callTool(t, set, "retention", map[string]any{})
	require.ErrorContains(t, err, "day is required")
}
