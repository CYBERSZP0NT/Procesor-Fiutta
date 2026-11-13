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

	"github.com/CYBERSZP0NT/szmelc-router/internal/config"
	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

type chatOpts struct {
	system      string
	noStream    bool
	maxTokens   int
	temperature float64
	showVia     bool
	mode        string
}

var chatOptsVar = &chatOpts{}

var chatCmd = &cobra.Command{
	Use:   `chat "prompt"`,
	Short: "Send a chat prompt to the LLM chain (streaming by default)",
	Long: `Send a prompt through the szmelc-router fallback chain and stream
the response to stdout. Uses the global --model flag to select a route
alias (e.g. "auto", "free", "smart") or a specific "provider/model".

Examples:
  szmelc-router chat "Hello!" --model free
  szmelc-router chat "Explain Go generics" --system "You are a Go tutor."
  szmelc-router chat "Write a poem" --no-stream --via
  szmelc-router --json chat "hi"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runChat,
}

func init() {
	chatCmd.Flags().StringVar(&chatOptsVar.system, "system", "", "system message (prepended to conversation)")
	chatCmd.Flags().BoolVarP(&chatOptsVar.noStream, "no-stream", "n", false, "disable streaming (wait for full response then print)")
	chatCmd.Flags().IntVar(&chatOptsVar.maxTokens, "max-tokens", 0, "max tokens to generate (0 = upstream default)")
	chatCmd.Flags().Float64Var(&chatOptsVar.temperature, "temperature", 0, "sampling temperature (0.0-2.0, 0 = upstream default)")
	chatCmd.Flags().BoolVar(&chatOptsVar.showVia, "via", false, "print the routing path used after the response")
	chatCmd.Flags().StringVar(&chatOptsVar.mode, "mode", "", "task-router mode for this call (rozklad-jazdy | jako-tako | live); implies --model agent when no model is given")
}

func runChat(cmd *cobra.Command, args []string) error {
	prompt := strings.Join(args, " ")

	cfg, err := config.Load(pCfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	r, rt, err := openRuntime(cfg, "cli")
	if err != nil {
		return err
	}
	defer rt.Close()
	session := os.Getenv("FREELLM_SESSION")

	messages := make([]router.Message, 0, 2)
	if chatOptsVar.system != "" {
		messages = append(messages, router.Message{Role: "system", Content: chatOptsVar.system})
	}
	messages = append(messages, router.Message{Role: "user", Content: prompt})

	selector := pModel
	if selector == "" {
		selector = defaultSelector(cfg)
		if chatOptsVar.mode != "" {
			selector = "agent"
		}
	}
	if chatOptsVar.mode != "" {
		if !r.IsTaskAlias(selector) {
			return fmt.Errorf("--mode needs a task-router model (agent), got %q", selector)
		}
		selector += ":" + chatOptsVar.mode
	}

	var maxTokens *int
	if chatOptsVar.maxTokens > 0 {
		maxTokens = &chatOptsVar.maxTokens
	}
	var temperature *float64
	if chatOptsVar.temperature > 0 {
		temperature = &chatOptsVar.temperature
	}

	var decision *router.TaskDecision
	opts := &router.RouteOpts{
		MaxTokens:   maxTokens,
		Temperature: temperature,
		Task:        &router.TaskHints{OnDecision: func(d *router.TaskDecision) { decision = d }},
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
		via, _, err = routeLogged(ctx, r, rt, session, selector, messages, opts, func(s string) {
			fmt.Print(s)
		})
		if err != nil {
			fmt.Fprintln(os.Stderr)
			return err
		}
		fmt.Println()
	} else {
		var err error
		var done *router.Done
		via, done, err = routeLogged(ctx, r, rt, session, selector, messages, opts, nil)
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
		if decision != nil {
			fmt.Fprintf(os.Stderr, "↳ task %s\n", decision.Reason)
			if decision.Winner != nil && decision.Via != "" && decision.Via != decision.Winner.Model {
				fmt.Fprintf(os.Stderr, "↳ %s failed or was unavailable, served by fallback %s\n", decision.Winner.Model, decision.Via)
			}
		}
	}

	return nil
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
