package vcrtest_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vcrtest"
)

// TestStubURL_RecordThenReplay walks the documented workflow end to end: while
// recording, StubURL serves the canned reply and Client captures it to a
// cassette; on replay, StubURL is an unreachable placeholder and the same
// request is answered from that cassette alone. It sets VCR_MODE and changes
// the working directory, so it cannot run in parallel.
func TestStubURL_RecordThenReplay(t *testing.T) {
	const (
		cassette  = "stub_roundtrip"
		stubReply = `{"reply":"from stub"}`
		reqBody   = `{"prompt":"hi"}`
	)
	t.Chdir(t.TempDir())

	t.Run("record", func(t *testing.T) {
		t.Setenv(vcrtest.EnvVCRMode, "record")
		base := vcrtest.StubURL(t, "application/json", stubReply)

		got := doPOST(t, vcrtest.Client(t, cassette), base+echoPath, reqBody, nil)

		if diff := cmp.Diff(decodeJSON(t, stubReply), decodeJSON(t, got)); diff != "" {
			t.Errorf("recorded reply from StubURL mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("replay", func(t *testing.T) {
		t.Setenv(vcrtest.EnvVCRMode, "")
		base := vcrtest.StubURL(t, "application/json", stubReply)
		if host := hostOf(t, base); !strings.HasSuffix(host, ".invalid") {
			t.Fatalf("StubURL() on replay = %q, want an unreachable .invalid host", base)
		}

		got := doPOST(t, vcrtest.Client(t, cassette), base+echoPath, reqBody, nil)

		if diff := cmp.Diff(decodeJSON(t, stubReply), decodeJSON(t, got)); diff != "" {
			t.Errorf("replayed reply mismatch (-want +got):\n%s", diff)
		}
	})
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) = %v", raw, err)
	}
	return u.Hostname()
}
