package agent

import (
	"context"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/require"
)

func TestChatKeepsConversationsApart(t *testing.T) {
	model := &fakeModel{responses: []*anthropic.Message{
		textResponse("a1"), textResponse("b1"), textResponse("a2"),
	}}
	chat := NewChat(model, "fake-model", func(string) []Tool { return nil })

	a, b := chat.Start(), chat.Start()
	require.NotEqual(t, a, b)

	for _, q := range []struct{ id, question, want string }{{a, "A?", "a1"}, {b, "B?", "b1"}, {a, "A again?", "a2"}} {
		answer, err := chat.Ask(context.Background(), q.id, q.question)
		require.NoError(t, err)
		require.Equal(t, q.want, answer)
	}
	// Conversation a's third call carries its own two questions and answer,
	// nothing from b.
	sent := wire(t, model.calls[2].Messages)
	require.Contains(t, sent, "A again?")
	require.NotContains(t, sent, "B?")

	_, err := chat.Ask(context.Background(), "chat-nope", "?")
	require.ErrorIs(t, err, ErrNoConversation)
}
