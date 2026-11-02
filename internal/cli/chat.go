package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/silverx/freellm-router/internal/config"
	"github.com/silverx/freellm-router/pkg/router"
)

type chatOpts struct {
	system      string
	noStream    bool
	maxTokens   int
	temperature float64
	showVia     bool
}

var chatOptsVar = &chatOpts{}

var chatCmd = &cobra.Command{
	Use:   `chat "prompt"`,
	Short: "Send a chat prompt to the LLM chain (streaming by default)",
	Long: `Send a prompt through the freellm-router fallback chain and stream
the response to stdout. Uses the global --model flag to select a route
alias (e.g. "auto", "free", "smart") or a specific "provider/model".

Examples:
  freellm-router chat "Hello!" --model free
  freellm-router chat "Explain Go generics" --system "You are a Go tutor."
  freellm-router chat "Write a poem" --no-stream --via
  freellm-router --json chat "hi"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runChat,
}

func init() {
	chatCmd.Flags().StringVar(&chatOptsVar.system, "system", "", "system message (prepended to conversation)")
	chatCmd.Flags().BoolVarP(&chatOptsVar.noStream, "no-stream", "n", false, "disable streaming (wait for full response then print)")
	chatCmd.Flags().IntVar(&chatOptsVar.maxTokens, "max-tokens", 0, "max tokens to generate (0 = upstream default)")
	chatCmd.Flags().Float64Var(&chatOptsVar.temperature, "temperature", 0, "sampling temperature (0.0-2.0, 0 = upstream default)")
	chatCmd.Flags().BoolVar(&chatOptsVar.showVia, "via", false, "print the routing path used after the response")
}

func runChat(cmd *cobra.Command, args []string) error {
	prompt := strings.Join(args, " ")

	cfg, err := config.Load(pCfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	r, err := buildRouter(cfg, globalTimeout())
	if err != nil {
		return fmt.Errorf("router: %w", err)
	}

	messages := make([]router.Message, 0, 2)
	if chatOptsVar.system != "" {
		messages = append(messages, router.Message{Role: "system", Content: chatOptsVar.system})
	}
	messages = append(messages, router.Message{Role: "user", Content: prompt})

	selector := pModel
	if selector == "" {
		selector = "auto"
	}

	var maxTokens *int
	if chatOptsVar.maxTokens > 0 {
		maxTokens = &chatOptsVar.maxTokens
	}
	var temperature *float64
	if chatOptsVar.temperature > 0 {
		temperature = &chatOptsVar.temperature
	}

	opts := &router.RouteOpts{
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	stream := !chatOptsVar.noStream && !pJSON

	var via string

	if stream {
		var err error
		via, _, err = r.RouteWithOptions(ctx, selector, messages, "", opts, func(s string) {
			fmt.Print(s)
		})
		if err != nil {
			fmt.Fprintln(os.Stderr)
			return err
		}
		fmt.Println()
	} else {
		var err error
		via, done, err := r.RouteWithOptions(ctx, selector, messages, "", opts, nil)
		if err != nil {
			return err
		}

		if pJSON {
			out := struct {
				Text   string `json:"text"`
				Via    string `json:"via"`
				Model  string `json:"model"`
				Finish string `json:"finish_reason"`
				Tokens struct {
					Prompt     int `json:"prompt_tokens"`
					Completion int `json:"completion_tokens"`
				} `json:"tokens"`
			}{
				Via:    via,
				Model:  selector,
				Finish: done.FinishReason,
			}
			out.Text = done.Text
			out.Tokens.Prompt = done.PromptTokens
			out.Tokens.Completion = done.CompletionTokens
			printJSON(out)
		} else {
			fmt.Println(done.Text)
		}
	}

	if chatOptsVar.showVia && !pJSON {
		fmt.Fprintf(os.Stderr, "\n↳ via %s\n", via)
	}

	return nil
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
