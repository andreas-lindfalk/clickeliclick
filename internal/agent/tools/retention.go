package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// retentionTool wraps Repository.Retention.
type retentionTool struct{ *Set }

func (retentionTool) Name() string { return "retention" }
func (retentionTool) Description() string {
	return "Retention for the cohort of users active on a given day: days[0] is the cohort size, days[i] how many of them were active again i days later. Use this for retention or 'did they come back' questions."
}
func (retentionTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"day":  map[string]any{"type": "string", "description": "Cohort day, 2026-09-01."},
			"days": map[string]any{"type": "integer", "description": "How many following days to report, 1-30. Default 7."},
		},
		"required": []string{"day"},
	}
}
func (r retentionTool) Call(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Day  string `json:"day"`
		Days int    `json:"days"`
	}
	if err := unmarshal(input, &in); err != nil {
		return "", err
	}
	if in.Day == "" {
		return "", fmt.Errorf("day is required, %s", timeFormats)
	}
	day, err := parseTime(in.Day, time.Time{})
	if err != nil {
		return "", err
	}
	if in.Days <= 0 {
		in.Days = 7
	}
	res, err := r.repo.Retention(r.tagged(ctx), day, in.Days)
	if err != nil {
		return "", err
	}
	return asJSON(res)
}
