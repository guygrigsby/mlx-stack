package router

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxy_NonStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"model":"upstream-name"`)) {
			t.Errorf("model not rewritten: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer upstream.Close()

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"alias","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	err := ProxyJSON(rr, req, upstream.URL, "upstream-name")
	if err != nil {
		t.Fatalf("ProxyJSON: %v", err)
	}
	resp := rr.Result()
	if resp.StatusCode != 200 {
		t.Errorf("status: %d", resp.StatusCode)
	}
}

func TestProxy_StreamsChunks(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			w.Write([]byte("data: chunk-" + string(rune('0'+i)) + "\n\n"))
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"x","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	if err := ProxyJSON(rr, req, upstream.URL, "upstream-name"); err != nil {
		t.Fatalf("ProxyJSON: %v", err)
	}
	body := rr.Body.String()
	if strings.Count(body, "data: chunk-") != 5 {
		t.Errorf("want 5 data chunks, got body: %q", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("missing [DONE]")
	}
}

func TestProxy_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer upstream.Close()

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	_ = ProxyJSON(rr, req, upstream.URL, "upstream-name")
	if rr.Code != 500 {
		t.Errorf("status: %d", rr.Code)
	}
}
