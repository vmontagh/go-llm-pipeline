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
	mux    *http.ServeMux
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
		mux:  mux,
		server: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// Handle registers handler for pattern on the server's mux, alongside the
// built-in pprof endpoints. It must be called before Start or StartListener,
// because the mux is fixed once the server begins serving. Use it to expose
// additional observability handlers such as the JSON counters from
// Metrics.Handler.
func (s *Server) Handle(pattern string, handler http.Handler) {
	s.mux.Handle(pattern, handler)
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

// StartListener runs the HTTP server on the given listener. This is a blocking
// call. It takes ownership of l and closes it on return, including when it
// rejects l's address before serving, as http.Server.Serve does.
func (s *Server) StartListener(l net.Listener) error {
	if err := checkLoopback(l.Addr()); err != nil {
		// The address error is what the caller needs; a close failure on a
		// listener that never served adds nothing actionable.
		_ = l.Close()
		return err
	}
	if err := s.server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("observability server error: %w", err)
	}
	return nil
}

// checkLoopback returns an error unless addr is a loopback host and port.
func checkLoopback(addr net.Addr) error {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return fmt.Errorf("invalid address: %w", err)
	}
	if host == "" {
		return fmt.Errorf("%w: got empty host", ErrPProfNonLoopback)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%w: got %q", ErrPProfNonLoopback, host)
	}
	return nil
}

// Stop shuts down the HTTP server gracefully.
func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
