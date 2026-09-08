package tools_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDescribeSchema(t *testing.T) {
	_, set := newTools(t)

	out, err := findTool(t, set, "describe_schema").Call(context.Background(), nil)
	require.NoError(t, err)

	require.Contains(t, out, "TABLE events (MergeTree, ORDER BY (event_type, user_id, ts))")
	require.Contains(t, out, "  ts DateTime64(3)")
	require.Contains(t, out, "  payload JSON(page String, ref LowCardinality(String))")
	require.Contains(t, out, "TABLE events_per_minute (AggregatingMergeTree")
	require.Contains(t, out, "TABLE users (ReplacingMergeTree")
	require.Contains(t, out, "users_dict")
	require.NotContains(t, out, "goose_db_version")
}
