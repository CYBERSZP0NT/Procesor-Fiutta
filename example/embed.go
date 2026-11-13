// Command embed shows how to embed the szmelc-router core in your own Go
// service (AGENTS.md STEP 8: < 30 lines): configure providers as plain data,
// Route() returns the served-by label plus the full text.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/silverx/szmelc-router/pkg/router"
)

func main() {
	r, err := router.New(router.Config{
		Providers: []router.ProviderConfig{
			{Name: "groq", BaseURL: "https://api.groq.com/openai/v1", KeyEnv: "GROQ_API_KEY", Kind: "keyed"},
			{Name: "poll", BaseURL: "https://text.pollinations.ai/openai", Kind: "keyless"},
		},
		Routes: map[string][]string{"auto": {"groq/llama-3.3-70b-versatile", "poll/openai"}},
	})
	if err != nil {
		log.Fatal(err)
	}
	var out string
	via, done, err := r.Route(context.Background(), "auto",
		[]router.Message{{Role: "user", Content: "Say hi in one word."}}, "",
		func(s string) { out += s })
	if err != nil {
		log.Printf("all providers failed: %v", err)
		return
	}
	finish := ""
	if done != nil {
		finish = done.FinishReason
	}
	fmt.Printf("via=%s finish=%q text=%q\n", via, finish, out)
}
