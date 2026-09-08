package tools_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"clickeliclick/internal/app"
)

// The SQL behind the curated tools is tested in internal/app. Here the
// question is the wrapping: inputs, defaults, output shape.

func TestFunnelTool(t *testing.T) {
	admin, set := newTools(t)
	repo := app.NewRepository(admin)
	ctx := context.Background()

	t0 := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Minute)
	require.NoError(t, repo.InsertEvents(ctx, []app.Event{
		{TS: t0, UserID: 1, EventType: "view"},
		{TS: t0.Add(5 * time.Minute), UserID: 1, EventType: "click"},
		{TS: t0.Add(10 * time.Minute), UserID: 1, EventType: "purchase"},
		{TS: t0, UserID: 2, EventType: "view"},
		{TS: t0.Add(2 * time.Hour), UserID: 2, EventType: "click"}, // outside a 1h window
	}))

	// Defaults: last 30 days, one hour window.
	out, err := callTool(t, set, "funnel", map[string]any{})
	require.NoError(t, err)
	require.JSONEq(t, `{"viewed":2,"clicked":1,"purchased":1}`, out)

	// A wider window catches user 2's late click.
	out, err = callTool(t, set, "funnel", map[string]any{"window_seconds": 3 * 3600})
	require.NoError(t, err)
	require.JSONEq(t, `{"viewed":2,"clicked":2,"purchased":1}`, out)

	// A window that excludes the data, with a date-only bound.
	out, err = callTool(t, set, "funnel", map[string]any{"to": t0.Add(-24 * time.Hour).Format(time.DateOnly)})
	require.NoError(t, err)
	require.JSONEq(t, `{"viewed":0,"clicked":0,"purchased":0}`, out)

	_, err = callTool(t, set, "funnel", map[string]any{"from": "yesterday"})
	require.ErrorContains(t, err, "bad time")
}
