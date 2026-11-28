package observ_test

import (
	"net"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/observ"
)

// fakeAddr is a net.Addr whose String form is fixed, so address validation can
// be exercised without binding a real socket.
type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

// addrListener is a net.Listener that only reports an address. Embedding the
// interface leaves Accept and Close unimplemented: StartListener must reject
// the address before it ever serves.
type addrListener struct {
	net.Listener
	addr fakeAddr
}

func (l addrListener) Addr() net.Addr { return l.addr }

func TestStartListenerRejectsNonLoopback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		addr    fakeAddr
		wantErr error
	}{
		{name: "public ipv4", addr: "203.0.113.7:6060", wantErr: observ.ErrPProfNonLoopback},
		{name: "unspecified ipv6", addr: "[::]:6060", wantErr: observ.ErrPProfNonLoopback},
		{name: "empty host", addr: ":6060", wantErr: observ.ErrPProfNonLoopback},
		{name: "non-localhost hostname", addr: "example.com:6060", wantErr: observ.ErrPProfNonLoopback},
		{name: "address without port", addr: "127.0.0.1", wantErr: cmpopts.AnyError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := observ.NewServer(string(tt.addr))

			err := srv.StartListener(addrListener{addr: tt.addr})

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("StartListener(%q) error mismatch (-want +got):\n%s", tt.addr, diff)
			}
		})
	}
}

func TestStartInvalidAddress(t *testing.T) {
	t.Parallel()
	const invalidAddr = "127.0.0.1:not-a-port"

	err := observ.NewServer(invalidAddr).Start()

	if diff := cmp.Diff(cmpopts.AnyError, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Start() on %q error mismatch (-want +got):\n%s", invalidAddr, diff)
	}
}
