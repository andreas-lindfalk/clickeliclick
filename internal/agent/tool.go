package agent

import (
	"context"
	"encoding/json"
)

// Tool is one capability the model may call. The shape mirrors what the
// Anthropic API expects in a tool definition: a name, a description the
// model reads to decide when to use it, and a JSON schema for the input.
//
// An error from Call is not a failure of the program. It goes back to the
// model as an error tool result, so a ClickHouse "limit for result exceeded"
// becomes something the model can read and react to.
//
// The tools themselves live in the tools subpackage; this package knows
// only the interface, and nothing about ClickHouse.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	Call(ctx context.Context, input json.RawMessage) (string, error)
}
