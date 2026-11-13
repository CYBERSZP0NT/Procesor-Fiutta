// Plugin: go-example
//
// A reference Go plugin implementing the szmelc-router JSON-RPC stdio protocol.
// Exposes two models:
//
//	– "echo"    — echoes the user message (streamed in 2-char chunks)
//	– "rotating"— cycles through a small list of canned responses
//
// Build:
//
//	cd plugins/go-example && go build -o ../../bin/go-example .
//
// Wire into szmelc-router config:
//
//	providers:
//	  - name: go-example
//	    impl: plugin
//	    kind: plugin
//	    command: ["/path/to/go-example"]
//	routes:
//	  go_test:
//	    - go-example/echo
//
// See docs/plugin-protocol.md for the full protocol spec.
package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/silverx/szmelc-router/pkg/pluginhost"
)

// ---------------------------------------------------------------------------
// Model catalogue
// ---------------------------------------------------------------------------

var catalog = []pluginhost.Model{
	{ID: "echo", Name: "Echo (echoes user message)"},
	{ID: "rotating", Name: "Rotating (cycles canned responses)"},
}

// ---------------------------------------------------------------------------
// Stream
// ---------------------------------------------------------------------------

var rotIdx int

func streamHandler(p *pluginhost.StreamParams, sink pluginhost.Sink) (*pluginhost.StreamDone, error) {
	userMsg := ""
	if len(p.Messages) > 0 {
		userMsg = p.Messages[len(p.Messages)-1].Content
	}

	switch p.Model {
	case "echo":
		// Stream in small chunks to exercise the notification protocol.
		text := "echo: " + userMsg
		for i := 0; i < len(text); i += 2 {
			end := i + 2
			if end > len(text) {
				end = len(text)
			}
			sink.Text(text[i:end])
		}
		return &pluginhost.StreamDone{FinishReason: "stop"}, nil

	case "rotating":
		msgs := []string{
			"The answer is 42.",
			"Rotating responses are the way to go.",
			"I am a very sophisticated plugin (allegedly).",
			"Thanks for the input: " + userMsg,
		}
		idx := rotIdx % len(msgs)
		rotIdx++
		msg := msgs[idx]
		words := strings.Fields(msg)
		for i, w := range words {
			if i > 0 {
				sink.Text(" ")
			}
			sink.Text(w)
		}
		return &pluginhost.StreamDone{FinishReason: "stop"}, nil

	default:
		return nil, fmt.Errorf("unknown model %q (available: echo, rotating)", p.Model)
	}
}

func availability() bool { return true }

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--test" {
		log.Println("go-example: --test mode, exiting")
		os.Exit(0)
	}

	h := pluginhost.New("go-example", "1.0.0")
	h.Models(func() ([]pluginhost.Model, error) { return catalog, nil })
	h.Available(availability)
	h.Stream(streamHandler)

	if err := h.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "go-example: run error:", err)
		os.Exit(1)
	}
}
