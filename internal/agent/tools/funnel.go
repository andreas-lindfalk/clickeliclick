package tools

import (
	"context"
	"encoding/json"
	"time"
)

// funnelTool wraps Repository.Funnel. See Tools.All for why curated tools
// exist next to run_sql.
type funnelTool struct{ *Set }

func (funnelTool) Name() string { return "funnel" }
func (funnelTool) Description() string {
	return "Conversion funnel view -> click -> purchase: how many users viewed, then clicked within the window after a view, then purchased within the window after that click. Use this for any funnel or conversion question."
}
func (funnelTool) InputSchema() map[string]any {
	props := windowProps()
	props["window_seconds"] = map[string]any{"type": "integer", "description": "Max seconds between steps. Default 3600."}
	return map[string]any{"type": "object", "properties": props}
}
func (f funnelTool) Call(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		window
		WindowSeconds int `json:"window_seconds"`
	}
	if err := unmarshal(input, &in); err != nil {
		return "", err
	}
	from, to, err := in.parse()
	if err != nil {
		return "", err
	}
	if in.WindowSeconds <= 0 {
		in.WindowSeconds = 3600
	}
	res, err := f.repo.Funnel(f.tagged(ctx), from, to, time.Duration(in.WindowSeconds)*time.Second)
	if err != nil {
		return "", err
	}
	return asJSON(res)
}
