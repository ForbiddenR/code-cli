package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
	"code-cli/internal/query"
)

func main() {
	fmt.Printf("BASE_URL=%q API_KEY_set=%v AUTH_TOKEN_set=%v MODEL=%q\n",
		os.Getenv("ANTHROPIC_BASE_URL"),
		os.Getenv("ANTHROPIC_API_KEY") != "",
		os.Getenv("ANTHROPIC_AUTH_TOKEN") != "",
		os.Getenv("ANTHROPIC_MODEL"),
	)
	client, err := anthropicapi.NewSDKClient(core.APIConfig{BaseURL: os.Getenv("ANTHROPIC_BASE_URL")})
	if err != nil {
		panic(err)
	}
	engine, err := query.NewEngine(query.Config{
		Client: client,
		Request: anthropicapi.MessageRequest{
			Model: core.ModelID("grok-4.5"),
		},
	})
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	result, err := engine.Run(ctx, "the model you are using", func(ev query.Event) {
		switch ev.Type {
		case query.EventStream:
			if ev.Stream != nil {
				fmt.Printf("stream %s\n", ev.Stream.Type)
			}
		case query.EventAssistantMessage:
			fmt.Printf("assistant blocks=%d\n", len(ev.Message.Content))
			for _, b := range ev.Message.Content {
				fmt.Printf("  type=%s text=%q\n", b.Type, truncate(b.Text, 80))
			}
		case query.EventCompleted:
			fmt.Printf("completed outcome=%v err=%v\n", ev.Result.Outcome, ev.Err)
		default:
			fmt.Printf("event %s\n", ev.Type)
		}
	})
	fmt.Printf("done elapsed=%s result.outcome=%s err=%v\n", time.Since(start), result.Outcome, err)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
