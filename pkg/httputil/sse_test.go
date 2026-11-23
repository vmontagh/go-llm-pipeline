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
	pr, pw := io.Pipe()
	restSent := make(chan struct{})
	go func() {
		// Each Write blocks until read, so restSent closes only after the
		// reader has taken the last event. The first event ends in CR, so
		// SSEData must not wait for a possible LF after it.
		for _, event := range []string{"data: a\r\r", "data: b\n\n"} {
			if _, err := pw.Write([]byte(event)); err != nil {
				t.Errorf("writing event: %v", err)
			}
		}
		close(restSent)
		pw.Close() // A pipe writer's Close always returns nil.
	}()

	next, stop := iter.Pull2(SSEData(pr))
	defer stop()
	data, err, _ := next()

	if err != nil {
		t.Fatalf("SSEData() error = %v", err)
	}
	select {
	case <-restSent:
		t.Errorf("event %q came after the whole stream was sent, want it as soon as it arrives", data)
	default:
	}
	// Drain so the writer can finish.
	if _, err := io.Copy(io.Discard, pr); err != nil {
		t.Errorf("draining stream: %v", err)
	}
}
