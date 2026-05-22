package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/spf13/cobra"
)

func loadCfg() *config.Config {
	path := os.Getenv("MLX_CONFIG")
	if path == "" {
		path = defaultConfigPathLocal()
	}
	c, err := config.Load(path)
	if err != nil {
		return nil
	}
	return c
}

func routerURL() string {
	if u := os.Getenv("MLXD_ROUTER"); u != "" {
		return u
	}
	if c := loadCfg(); c != nil && c.Router.Port > 0 {
		host := c.Router.Host
		if host == "" {
			host = "127.0.0.1"
		}
		return fmt.Sprintf("http://%s:%d", host, c.Router.Port)
	}
	return "http://127.0.0.1:8080"
}

// resolveChatModel picks a model name to send to the router. Prefers a
// currently-loaded LM swap member from /v1/status; falls back to the
// configured default member of group "chat" (or, failing that, any LM swap
// member). Returns "" if nothing usable is found.
func resolveChatModel() string {
	statusBody, _ := newClient().Get(context.Background(), "/v1/status")
	if statusBody != nil {
		var s struct {
			Backends []struct {
				Name        string `json:"name"`
				Group       string `json:"group"`
				Mode        string `json:"mode"`
				Engine      string `json:"engine"`
				Running     bool   `json:"running"`
				CurrentName string `json:"current_name,omitempty"`
			} `json:"backends"`
		}
		if json.Unmarshal(statusBody, &s) == nil {
			for _, b := range s.Backends {
				if b.Mode == "swap" && b.Engine == "lm" && b.CurrentName != "" {
					return b.CurrentName
				}
			}
		}
	}

	c := loadCfg()
	if c == nil {
		return ""
	}
	var firstLM string
	for _, b := range c.Backends {
		if b.Mode != "swap" || b.Engine != "lm" {
			continue
		}
		if firstLM == "" {
			firstLM = b.Name
		}
		if b.Group == "chat" && b.Default {
			return b.Name
		}
	}
	for _, b := range c.Backends {
		if b.Mode == "swap" && b.Engine == "lm" && b.Default {
			return b.Name
		}
	}
	return firstLM
}

// samplerFor returns the configured sampler for the given backend name, or
// nil if no override exists.
func samplerFor(name string) *config.Sampler {
	c := loadCfg()
	if c == nil {
		return nil
	}
	for _, b := range c.Backends {
		if b.Name == name && b.Sampler != nil {
			return b.Sampler
		}
	}
	return nil
}

// applySampler merges non-zero sampler params into the request payload.
func applySampler(payload map[string]any, s *config.Sampler) {
	if s == nil {
		return
	}
	if s.Temperature != 0 {
		payload["temperature"] = s.Temperature
	}
	if s.TopP != 0 {
		payload["top_p"] = s.TopP
	}
	if s.TopK != 0 {
		payload["top_k"] = s.TopK
	}
	if s.MinP != 0 {
		payload["min_p"] = s.MinP
	}
	if s.RepetitionPenalty != 0 {
		payload["repetition_penalty"] = s.RepetitionPenalty
	}
	if s.MaxTokens != 0 {
		payload["max_tokens"] = s.MaxTokens
	}
}

func newChatCmd() *cobra.Command {
	var noStream bool
	cmd := &cobra.Command{
		Use:   "chat \"...\"",
		Short: "Send a chat request via the router (streams by default)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			model := resolveChatModel()
			if model == "" {
				return fmt.Errorf("no chat-capable backend found (need a swap-mode lm backend in config or running)")
			}

			msg := strings.Join(args, " ")
			payload := map[string]any{
				"model": model,
				"messages": []map[string]string{
					{"role": "user", "content": msg},
				},
				"stream": !noStream,
			}
			applySampler(payload, samplerFor(model))
			if _, ok := payload["max_tokens"]; !ok {
				payload["max_tokens"] = 512
			}
			body, _ := json.Marshal(payload)

			resp, err := http.Post(routerURL()+"/v1/chat/completions", "application/json", bytes.NewReader(body))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				respBody, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("status %d: %s", resp.StatusCode, respBody)
			}

			if noStream {
				respBody, _ := io.ReadAll(resp.Body)
				printNonStreamChat(respBody)
				return nil
			}
			return streamChatSSE(resp.Body)
		},
	}
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "buffer the full reply instead of streaming tokens")
	return cmd
}

func printNonStreamChat(respBody []byte) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil || len(parsed.Choices) == 0 {
		fmt.Println(string(respBody))
		return
	}
	fmt.Println(parsed.Choices[0].Message.Content)
}

// streamChatSSE reads an OpenAI-style chat-completion SSE stream and prints
// the assistant content deltas to stdout as they arrive.
func streamChatSSE(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	any := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				fmt.Print(c.Delta.Content)
				any = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if any {
		fmt.Println()
	}
	return nil
}

func newTagsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tags",
		Short: "List available models from the router",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Get(routerURL() + "/v1/models")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			var list struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			json.Unmarshal(body, &list)
			for _, m := range list.Data {
				fmt.Println(m.ID)
			}
			return nil
		},
	}
}
