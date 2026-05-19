package ipc

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestClient_RoundTripsOverUnixSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"got":"` + r.URL.Path + `"}`))
	})}).Serve(ln)

	c := New(sock)
	body, err := c.Get(context.Background(), "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "/v1/status") {
		t.Errorf("body: %s", body)
	}
}

func TestClient_PostJSON(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "sock")
	ln, _ := net.Listen("unix", sock)
	defer ln.Close()

	go (&http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		w.Write(buf[:n])
	})}).Serve(ln)

	c := New(sock)
	body, err := c.PostJSON(context.Background(), "/v1/swap", []byte(`{"profile":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "p1") {
		t.Errorf("body: %s", body)
	}
}

func TestClient_ConnectionRefused(t *testing.T) {
	c := New("/nonexistent.sock")
	_, err := c.Get(context.Background(), "/v1/health")
	if err == nil {
		t.Fatal("expected error")
	}
}
