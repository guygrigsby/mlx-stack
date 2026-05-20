package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func routerURL() string {
	if u := os.Getenv("MLXD_ROUTER"); u != "" {
		return u
	}
	return "http://127.0.0.1:1231"
}

func newChatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "chat \"...\"",
		Short: "Send a chat request via the router",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Try to discover the default chat profile via /v1/status; fall back to
			// using the first profile listed.
			statusBody, _ := newClient().Get(context.Background(), "/v1/status")
			defaultProfile := ""
			if statusBody != nil {
				var s struct {
					Chat struct {
						CurrentProfile string   `json:"current_profile"`
						Profiles       []string `json:"profiles"`
					} `json:"chat"`
				}
				if json.Unmarshal(statusBody, &s) == nil {
					if s.Chat.CurrentProfile != "" {
						defaultProfile = s.Chat.CurrentProfile
					} else if len(s.Chat.Profiles) > 0 {
						defaultProfile = s.Chat.Profiles[0]
					}
				}
			}
			if defaultProfile == "" {
				return fmt.Errorf("could not determine default chat profile")
			}

			msg := strings.Join(args, " ")
			body, _ := json.Marshal(map[string]any{
				"model": defaultProfile,
				"messages": []map[string]string{
					{"role": "user", "content": msg},
				},
				"max_tokens": 512,
			})
			resp, err := http.Post(routerURL()+"/v1/chat/completions", "application/json", bytes.NewReader(body))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			respBody, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				return fmt.Errorf("status %d: %s", resp.StatusCode, respBody)
			}

			// Extract assistant content.
			var parsed struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(respBody, &parsed); err != nil {
				fmt.Println(string(respBody))
				return nil
			}
			if len(parsed.Choices) > 0 {
				fmt.Println(parsed.Choices[0].Message.Content)
			} else {
				fmt.Println(string(respBody))
			}
			return nil
		},
	}
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
