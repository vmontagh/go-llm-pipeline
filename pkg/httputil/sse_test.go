package httputil

import (
	"errors"
	"io"
	"iter"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var errRead = errors.New("read failed")

func TestSSEData(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       string
		failRead bool // reading fails after in
		want     []string
		wantErr  error
	}{
		{name: "events", in: "data: a\n\ndata: b\n\n", want: []string{"a", "b"}},
		{name: "multiLineData", in: "data: a\ndata: b\n\n", want: []string{"a\nb"}},
		{name: "crlf", in: "data: a\r\n\r\n", want: []string{"a"}},
		{name: "crOnly", in: "data: a\rdata: b\r\rdata: c\r\r", want: []string{"a\nb", "c"}},
		{name: "mixedEndings", in: "data: a\r\ndata: b\n\r\r\n", want: []string{"a\nb"}},
		{name: "bom", in: "\uFEFFdata: a\n\n", want: []string{"a"}},
		{name: "noSpaceAfterColon", in: "data:a\n\n", want: []string{"a"}},
		{name: "otherFieldsSkipped", in: ": ping\nevent: x\nid: 1\ndata: a\n\n", want: []string{"a"}},
		{name: "eventWithoutDataSkipped", in: "event: x\n\ndata: a\n\n", want: []string{"a"}},
		{name: "cutOffEvent", in: "data: a\n\ndata: b\n", want: []string{"a"}, wantErr: io.ErrUnexpectedEOF},
		{name: "cutOffLine", in: "data: a", wantErr: io.ErrUnexpectedEOF},
		{name: "trailingComment", in: "data: a\n\n: bye", want: []string{"a"}},
		{name: "readError", in: "data: a\n\n", failRead: true, want: []string{"a"}, wantErr: errRead},
		{name: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			var gotErr error
			var r io.Reader = strings.NewReader(tt.in)
			if tt.failRead {
				r = io.MultiReader(r, iotest.ErrReader(errRead))
			}
			for data, err := range SSEData(r) {
				if err != nil {
					gotErr = err
					continue
				}
				got = append(got, string(data))
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("SSEData(%q) events (-want +got):\n%s", tt.in, diff)
			}
			if diff := cmp.Diff(tt.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("SSEData(%q) error (-want +got):\n%s", tt.in, diff)
			}
		})
	}
}

func TestSSEDataDeliversIncrementally(t *testing.T) {
	t.Parallel()
	// The event ends in CR, so SSEData must not wait for a possible LF.
	r := newStallReader("data: a\r\r")
	next, stop := iter.Pull2(SSEData(r))
	defer stop()
	type result struct {
		data []byte
		err  error
	}
	got := make(chan result, 1)
	go func() {
		data, err, _ := next()
		got <- result{data, err}
	}()

	select {
	case res := <-got:
		if diff := cmp.Diff(result{data: []byte("a")}, res, cmp.AllowUnexported(result{}), cmpopts.EquateErrors()); diff != "" {
			t.Errorf("SSEData() first event (-want +got):\n%s", diff)
		}
	case <-r.stalled:
		t.Error("SSEData() read past the first event before yielding it")
		r.release()
		<-got
	}
}

// stallReader returns its data, then blocks until released. stalled is
// closed when a read waits for more.
type stallReader struct {
	data     []byte
	stalled  chan struct{}
	released chan struct{}
}

func newStallReader(data string) *stallReader {
	return &stallReader{data: []byte(data), stalled: make(chan struct{}), released: make(chan struct{})}
}

func (r *stallReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	close(r.stalled)
	<-r.released
	return 0, io.EOF
}

// release ends the stalled read with io.EOF.
func (r *stallReader) release() { close(r.released) }
