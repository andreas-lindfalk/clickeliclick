package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
)

// ToolFactory builds the tool set for one conversation, so each
// conversation's queries can carry its id.
type ToolFactory func(conversationID string) []Tool

// Chat keeps conversations in memory, each with its own tool set. A restart
// forgets everything, which is fine for a POC; the audit trail in
// system.query_log survives.
type Chat struct {
	model    Model
	modelID  string
	newTools ToolFactory

	// OnToolCall is copied to every agent created; see Agent.OnToolCall.
	OnToolCall func(name string, input json.RawMessage, output string, err error)

	mu    sync.Mutex
	convs map[string]*conversation
}

type conversation struct {
	mu   sync.Mutex // one question at a time per conversation
	conv *Conversation
}

func NewChat(model Model, modelID string, newTools ToolFactory) *Chat {
	return &Chat{model: model, modelID: modelID, newTools: newTools, convs: map[string]*conversation{}}
}

// Start opens a conversation and returns its id.
func (c *Chat) Start() string {
	id := "chat-" + randomHex(4)
	a := New(c.model, c.modelID, c.newTools(id))
	a.OnToolCall = c.OnToolCall

	c.mu.Lock()
	defer c.mu.Unlock()
	c.convs[id] = &conversation{conv: a.NewConversation()}
	return id
}

var ErrNoConversation = errors.New("no such conversation")

// Ask puts a question to an existing conversation.
func (c *Chat) Ask(ctx context.Context, id, question string) (string, error) {
	c.mu.Lock()
	cv, ok := c.convs[id]
	c.mu.Unlock()
	if !ok {
		return "", ErrNoConversation
	}
	cv.mu.Lock()
	defer cv.mu.Unlock()
	return cv.conv.Ask(ctx, question)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
