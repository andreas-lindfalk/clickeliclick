package tools

import (
	"context"
	"encoding/json"
)

// topUsersTool wraps Repository.TopUsersByCountry.
type topUsersTool struct{ *Set }

func (topUsersTool) Name() string { return "top_users" }
func (topUsersTool) Description() string {
	return "The n most active users per country in the window, with their event count and the last page they visited. Countries come from the users dictionary."
}
func (topUsersTool) InputSchema() map[string]any {
	props := windowProps()
	props["n"] = map[string]any{"type": "integer", "description": "Users per country, 1-20. Default 3."}
	return map[string]any{"type": "object", "properties": props}
}
func (t topUsersTool) Call(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		window
		N int `json:"n"`
	}
	if err := unmarshal(input, &in); err != nil {
		return "", err
	}
	from, to, err := in.parse()
	if err != nil {
		return "", err
	}
	if in.N <= 0 {
		in.N = 3
	}
	if in.N > 20 {
		in.N = 20
	}
	res, err := t.repo.TopUsersByCountry(t.tagged(ctx), from, to, in.N)
	if err != nil {
		return "", err
	}
	return asJSON(res)
}
