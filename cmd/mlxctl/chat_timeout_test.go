package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A wedged backend accepts the TCP connection but never sends a response
// header. mlxctl must not hang forever: postChat uses a client with a
// response-header timeout and returns an error instead of blocking.
func TestPostChat_WedgedBackendTimesOut(t *testing.T) {
	// Handler blocks (never sends a header) until the test ends. The stop
	// channel is closed before srv.Close() (defer LIFO) so Close never waits
	// on an in-flight handler.
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(stop)

	client := newChatClient(50 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		resp, err := postChat(client, srv.URL+"/v1/chat/completions", []byte(`{"model":"x"}`))
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error from a wedged backend, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("postChat hung past the response-header timeout")
	}
}
