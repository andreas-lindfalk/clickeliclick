// Package tools holds what the model may do against ClickHouse: a schema
// description, free SQL, and a few curated queries wrapping repository
// methods. Every tool runs through one connection authenticated as the
// restricted user from migration 007 and tags its queries with the
// conversation id. One file per tool.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	cl "github.com/ClickHouse/clickhouse-go/v2"

	"clickeliclick/internal/agent"
	"clickeliclick/internal/app"
	"clickeliclick/internal/pkg/clickhouse"
)

// Set is what every tool shares: the connection, a repository on top of it
// for the curated tools, and the conversation id.
type Set struct {
	client         *clickhouse.Client
	repo           *app.Repository
	conversationID string
}

// New binds a tool set to a connection, which must be authenticated as the
// restricted user, and to one conversation.
func New(client *clickhouse.Client, conversationID string) *Set {
	return &Set{client: client, repo: app.NewRepository(client), conversationID: conversationID}
}

// ConversationID is the log_comment every query from this set carries.
func (s *Set) ConversationID() string { return s.conversationID }

// All returns the tools in the order they are offered to the model.
//
// Two kinds. The curated tools (funnel, retention, top_users) wrap
// repository methods: the model fills in parameters, the SQL and its cost
// are ours. run_sql lets the model write the query itself, for the long
// tail of questions, fenced by the ClickHouse user rather than by code.
// Both kinds run through the same restricted connection and carry the
// conversation tag, so grants and audit apply to trusted queries too.
func (s *Set) All() []agent.Tool {
	return []agent.Tool{describeSchema{s}, funnelTool{s}, retentionTool{s}, topUsersTool{s}, runSQL{s}}
}

// tagged stamps the conversation id on every query as log_comment and hides
// any deadline from the driver (see noDeadline).
func (s *Set) tagged(ctx context.Context) context.Context {
	return cl.Context(noDeadline{ctx}, cl.WithSettings(cl.Settings{"log_comment": s.conversationID}))
}

// noDeadline hides the context deadline from the driver but keeps its
// cancellation. clickhouse-go turns a deadline into a max_execution_time
// query setting, which the readonly profile rejects. The server's own
// max_execution_time is the timeout here; the caller's deadline still
// cancels the wait on our side.
type noDeadline struct{ context.Context }

func (noDeadline) Deadline() (time.Time, bool) { return time.Time{}, false }

// Input and output helpers shared by the tools.

func unmarshal(input json.RawMessage, v any) error {
	if len(input) == 0 {
		return nil
	}
	if err := json.Unmarshal(input, v); err != nil {
		return fmt.Errorf("invalid input: %w", err)
	}
	return nil
}

func asJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

const timeFormats = "RFC3339 (2026-09-08T12:00:00Z) or a date (2026-09-08)"

func parseTime(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("bad time %q, want %s", s, timeFormats)
}

// window is the from/to input shared by the tools that take a time range,
// defaulting to the last 30 days, which is also what the row policy allows.
type window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (w window) parse() (from, to time.Time, err error) {
	if to, err = parseTime(w.To, time.Now()); err != nil {
		return from, to, err
	}
	from, err = parseTime(w.From, to.Add(-30*24*time.Hour))
	return from, to, err
}

func windowProps() map[string]any {
	return map[string]any{
		"from": map[string]any{"type": "string", "description": "Start, " + timeFormats + ". Default: 30 days before to."},
		"to":   map[string]any{"type": "string", "description": "End, " + timeFormats + ". Default: now."},
	}
}
