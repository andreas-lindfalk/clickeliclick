package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/require"
)

// The loop is tested without the API and without ClickHouse: a scripted
// model and an echo tool are enough to see the messages it builds.

type fakeModel struct {
	responses []*anthropic.Message
	calls     []anthropic.MessageNewParams
}

func (f *fakeModel) New(_ context.Context, params anthropic.MessageNewParams, _ ...option.RequestOption) (*anthropic.Message, error) {
	f.calls = append(f.calls, params)
	if len(f.responses) == 0 {
		return nil, errors.New("fake model: no scripted response left")
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	return r, nil
}

func textResponse(text string) *anthropic.Message {
	return &anthropic.Message{StopReason: "end_turn", Content: []anthropic.ContentBlockUnion{{Type: "text", Text: text}}}
}

func toolResponse(id, name, input string) *anthropic.Message {
	return &anthropic.Message{StopReason: "tool_use", Content: []anthropic.ContentBlockUnion{
		{Type: "text", Text: "Let me check."},
		{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(input)},
	}}
}

// echoTool returns its input, or fails when asked to.
type echoTool struct{ inputs []string }

func (echoTool) Name() string                { return "echo" }
func (echoTool) Description() string         { return "echoes" }
func (echoTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (e *echoTool) Call(_ context.Context, in json.RawMessage) (string, error) {
	e.inputs = append(e.inputs, string(in))
	var v struct{ Fail bool }
	_ = json.Unmarshal(in, &v)
	if v.Fail {
		return "", errors.New("boom")
	}
	return "echo:" + string(in), nil
}

// wire renders what the loop would send, so tests can assert on the JSON
// the API sees rather than on SDK union structs.
func wire(t *testing.T, msgs []anthropic.MessageParam) string {
	t.Helper()
	b, err := json.Marshal(msgs)
	require.NoError(t, err)
	return string(b)
}

func TestAskRunsToolsUntilTextAnswer(t *testing.T) {
	model := &fakeModel{responses: []*anthropic.Message{
		toolResponse("t1", "echo", `{"x":1}`),
		textResponse("The answer is 1."),
	}}
	echo := &echoTool{}
	a := New(model, "fake-model", []Tool{echo})
	conv := a.NewConversation()

	answer, err := conv.Ask(context.Background(), "what is x?")
	require.NoError(t, err)
	require.Equal(t, "The answer is 1.", answer)
	require.Equal(t, []string{`{"x":1}`}, echo.inputs)

	// Second model call carries: user question, assistant tool_use, user
	// tool_result. That is the whole protocol.
	require.Len(t, model.calls, 2)
	sent := wire(t, model.calls[1].Messages)
	require.Contains(t, sent, `{"id":"t1","input":{"x":1},"name":"echo","type":"tool_use"}`)
	require.Contains(t, sent, `"tool_use_id":"t1","is_error":false,"content":[{"text":"echo:{\"x\":1}","type":"text"}],"type":"tool_result"`)

	// The tool definition goes with every call.
	tools, err := json.Marshal(model.calls[0].Tools)
	require.NoError(t, err)
	require.Contains(t, string(tools), `"name":"echo"`)

	// A follow-up question keeps the history: question, tool_use, result,
	// answer, follow-up.
	model.responses = []*anthropic.Message{textResponse("Still 1.")}
	_, err = conv.Ask(context.Background(), "sure?")
	require.NoError(t, err)
	require.Len(t, model.calls[2].Messages, 5)
}

func TestAskReportsToolErrorsToTheModel(t *testing.T) {
	model := &fakeModel{responses: []*anthropic.Message{
		toolResponse("t1", "echo", `{"Fail":true}`),
		toolResponse("t2", "nope", `{}`),
		textResponse("Could not do it."),
	}}
	a := New(model, "fake-model", []Tool{&echoTool{}})

	answer, err := a.NewConversation().Ask(context.Background(), "try")
	require.NoError(t, err)
	require.Equal(t, "Could not do it.", answer)

	// A failing tool and an unknown tool both become error results, not
	// program errors; the model gets to read them.
	require.Contains(t, wire(t, model.calls[1].Messages), `"is_error":true,"content":[{"text":"boom","type":"text"}]`)
	require.Contains(t, wire(t, model.calls[2].Messages), `"is_error":true,"content":[{"text":"unknown tool nope","type":"text"}]`)
}

func TestAskGivesUpAfterMaxTurns(t *testing.T) {
	var responses []*anthropic.Message
	for i := 0; i < defaultMaxTurns+1; i++ {
		responses = append(responses, toolResponse("t", "echo", `{}`))
	}
	model := &fakeModel{responses: responses}
	a := New(model, "fake-model", []Tool{&echoTool{}})

	_, err := a.NewConversation().Ask(context.Background(), "loop forever")
	require.ErrorContains(t, err, "no answer after")
	require.Len(t, model.calls, defaultMaxTurns)
}
