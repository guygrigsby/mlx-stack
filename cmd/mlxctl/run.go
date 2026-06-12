package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/guygrigsby/mlx-stack/internal/config"
	"github.com/spf13/cobra"
)

// newRunCmd drives inference against any lm/vlm backend by name (or group name),
// unlike `chat` which auto-picks the loaded chat model. The router loads the
// target on demand, so no explicit swap is needed.
func newRunCmd() *cobra.Command {
	var (
		noStream  bool
		images    []string
		maxTokens int
	)
	cmd := &cobra.Command{
		Use:   "run <model> \"...\"",
		Short: "Run inference against any lm/vlm backend by name (streams by default)",
		Long: "Run a prompt against a specific backend or group by name, e.g. " +
			"`mlxctl run coder \"write a fib fn\"` or `mlxctl run exp \"what's this?\" --image cat.png`. " +
			"The router loads the model on demand. Use --image (repeatable) for vision models; " +
			"local paths are inlined, http(s) URLs are passed through.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			model := args[0]
			prompt := strings.Join(args[1:], " ")

			// Guard against pointing `run` at an engine it can't drive. Unknown
			// names are sent anyway and the router returns its own error.
			if cfg := loadCfg(); cfg != nil {
				if engine, ok := runModelEngine(cfg, model); ok {
					if err := checkRunEngine(engine); err != nil {
						return err
					}
				}
			}

			content, err := buildContent(prompt, images)
			if err != nil {
				return err
			}
			payload := buildChatPayload(model, content, !noStream, samplerFor(model))
			if maxTokens > 0 {
				payload["max_tokens"] = maxTokens
			}
			return sendChatCompletion(model, payload, noStream)
		},
	}
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "buffer the full reply instead of streaming tokens")
	cmd.Flags().StringArrayVar(&images, "image", nil, "image path or http(s) URL for vision models (repeatable)")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0, "max output tokens (overrides the backend sampler/default)")
	return cmd
}

// runModelEngine resolves a model name to its engine by matching either a
// backend name or a swap group name in the config. The bool is false when no
// backend matches.
func runModelEngine(cfg *config.Config, model string) (string, bool) {
	for _, b := range cfg.Backends {
		if b.Name == model || (b.Group != "" && b.Group == model) {
			return b.Engine, true
		}
	}
	return "", false
}

// checkRunEngine rejects engines whose I/O `run` doesn't handle, pointing at the
// verb that will. lm and vlm both speak chat-completions and are accepted.
func checkRunEngine(engine string) error {
	switch engine {
	case "lm", "vlm":
		return nil
	case "audio":
		return fmt.Errorf("model engine is audio; `run` handles lm/vlm only (a `speak` verb is planned for TTS)")
	case "embed":
		return fmt.Errorf("model engine is embed; `run` handles lm/vlm only (an `embed` verb is planned)")
	default:
		return fmt.Errorf("model engine %q is not supported by `run` (lm/vlm only)", engine)
	}
}

// buildContent returns the chat message content: a plain prompt string when
// there are no images, or an OpenAI multimodal parts array (text first, then one
// image_url part per image) otherwise.
func buildContent(prompt string, images []string) (any, error) {
	if len(images) == 0 {
		return prompt, nil
	}
	parts := []map[string]any{{"type": "text", "text": prompt}}
	for _, img := range images {
		url, err := imageToURL(img)
		if err != nil {
			return nil, err
		}
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": url},
		})
	}
	return parts, nil
}

// imageToURL turns an --image value into something the API accepts: http(s)
// URLs pass through unchanged; a local file is read and inlined as a base64
// data URL with its MIME type sniffed from the bytes.
func imageToURL(s string) (string, error) {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s, nil
	}
	data, err := os.ReadFile(s)
	if err != nil {
		return "", fmt.Errorf("read image %q: %w", s, err)
	}
	mime := http.DetectContentType(data)
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
