// Command chat is a terminal REPL in front of the agent. It prints every
// tool call the model makes, because seeing the SQL it writes is the point.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/anthropics/anthropic-sdk-go"

	"clickeliclick/internal/agent"
	"clickeliclick/internal/agent/tools"
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

	client := anthropic.NewClient() // reads ANTHROPIC_API_KEY
	chat := agent.NewChat(&client.Messages, modelID, func(id string) []agent.Tool { return tools.New(ch, id).All() })
	chat.OnToolCall = func(name string, input json.RawMessage, output string, err error) {
		fmt.Printf("\033[2m  %s %s\n", name, input)
		if err != nil {
			fmt.Printf("  error: %v\033[0m\n", err)
			return
		}
		fmt.Printf("  %d bytes\033[0m\n", len(output))
	}
	conversationID := chat.Start()

	fmt.Printf("model %s, conversation %s\n", modelID, conversationID)
	fmt.Printf("audit: SELECT query FROM system.query_log WHERE log_comment = '%s'\n\n", conversationID)

	in := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")
	for in.Scan() {
		q := in.Text()
		if q != "" {
			answer, err := chat.Ask(ctx, conversationID, q)
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
	if err := in.Err(); err != nil {
		log.Fatalf("read stdin: %v", err)
	}
}
