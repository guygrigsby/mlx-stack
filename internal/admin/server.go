package admin

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

type Server struct {
	SocketPath string
	Handler    http.Handler

	listener net.Listener
	server   *http.Server
}

func (s *Server) Start() error {
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(s.SocketPath)
	ln, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.SocketPath, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.listener = ln
	s.server = &http.Server{Handler: s.Handler}
	go func() {
		err := s.server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// best-effort; log via slog at caller if needed
		}
	}()
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	err := s.server.Shutdown(ctx)
	_ = os.Remove(s.SocketPath)
	return err
}
