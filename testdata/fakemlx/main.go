package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	model := flag.String("model", "", "")
	host := flag.String("host", "127.0.0.1", "")
	port := flag.Int("port", 0, "")
	streamChunks := flag.Int("chunks", 5, "number of SSE chunks per chat completion")
	flag.Parse()

	if *model == "" || *port == 0 {
		fmt.Fprintln(os.Stderr, "fakemlx: --model and --port required")
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "[mlx-launch] starting engine=fake model=%s port=%d\n", *model, *port)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": *model, "object": "model"}},
		})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		streaming := strings.Contains(string(body), `"stream":true`)
		if !streaming {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":      "fake-1",
				"object":  "chat.completion",
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < *streamChunks; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok-%d \"}}]}\n\n", i)
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	})
	mux.HandleFunc("POST /v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("FAKE-AUDIO-BYTES"))
	})
	mux.HandleFunc("POST /v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = body // fakemlx is content-agnostic
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"object": "embedding", "embedding": []float64{0.1, 0.2, 0.3}, "index": 0},
			},
			"model": *model,
			"usage": map[string]int{"prompt_tokens": 3, "total_tokens": 3},
		})
	})

	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", *host, *port), Handler: mux}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	fmt.Fprintln(os.Stderr, "[mlx-launch] mem: active=1000000 cache=0 peak=1000000")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "fakemlx: %v\n", err)
		os.Exit(1)
	}
}
