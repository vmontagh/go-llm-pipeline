// Package observ provides functionality for observ.
package observ

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// Server encapsulates the observability HTTP server.
type Server struct {
	addr   string
	server *http.Server
}

// ErrPProfNonLoopback is returned when Start is invoked with a host that is not loopback.
var ErrPProfNonLoopback = errors.New("pprof server must bind to loopback address")

// NewServer creates a new observability server.
func NewServer(addr string) *Server {
	mux := http.NewServeMux()

	// Register pprof handlers
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	return &Server{
		addr: addr,
		server: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// Start runs the HTTP server. This is a blocking call.
func (s *Server) Start() error {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}
	return s.StartListener(l)
}

// StartListener runs the HTTP server on the given listener. This is a blocking call.
func (s *Server) StartListener(l net.Listener) error {
	host, _, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return fmt.Errorf("invalid address: %w", err)
	}
	if host == "" {
		return fmt.Errorf("%w: got empty host", ErrPProfNonLoopback)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("%w: got %q", ErrPProfNonLoopback, host)
		}
	}
	if err := s.server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("observability server error: %w", err)
	}
	return nil
}

// Stop shuts down the HTTP server gracefully.
func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
