// Command chat is a terminal REPL in front of the agent. It prints every
// tool call the model makes, because seeing the SQL it writes is the point.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/anthropics/anthropic-sdk-go"

	"clickeliclick/internal/agent"
	"clickeliclick/internal/pkg/clickhouse"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("ANTHROPIC_API_KEY is not set")
	}
	modelID := os.Getenv("ANTHROPIC_MODEL")
	if modelID == "" {
		modelID = "claude-sonnet-5"
	}

	// The agent connects as the restricted user, never as the service user.
	ch, err := clickhouse.New(ctx, clickhouse.AgentConfigFromEnv())
	if err != nil {
		log.Fatalf("connect clickhouse: %v", err)
	}
	defer ch.Close()

	conversationID := "chat-" + randomHex(4)
	tools := agent.NewTools(ch, conversationID)

	client := anthropic.NewClient() // reads ANTHROPIC_API_KEY
	a := agent.New(&client.Messages, modelID, tools.All())
	a.OnToolCall = func(name string, input json.RawMessage, output string, err error) {
		fmt.Printf("\033[2m  %s %s\n", name, input)
		if err != nil {
			fmt.Printf("  error: %v\033[0m\n", err)
			return
		}
		fmt.Printf("  %d bytes\033[0m\n", len(output))
	}
	conv := a.NewConversation()

	fmt.Printf("model %s, conversation %s\n", modelID, conversationID)
	fmt.Printf("audit: SELECT query FROM system.query_log WHERE log_comment = '%s'\n\n", conversationID)

	in := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")
	for in.Scan() {
		q := in.Text()
		if q != "" {
			answer, err := conv.Ask(ctx, q)
			if err != nil {
				fmt.Printf("error: %v\n", err)
			} else {
				fmt.Printf("\n%s\n", answer)
			}
		}
		if ctx.Err() != nil {
			return
		}
		fmt.Print("\n> ")
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
