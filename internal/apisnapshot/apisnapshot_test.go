package apisnapshot_test

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/internal/apisnapshot"
)

const (
	modulePath  = "github.com/vmontagh/go-llm-pipeline"
	repoRoot    = "../.."
	snapshotDir = "api"
	permFile    = 0o600
)

var update = flag.Bool("update", false, "rewrite the API snapshots in api/")

// stablePackages are the packages whose exported API is frozen. Changing one
// needs a snapshot update (make api) that reviewers can see.
var stablePackages = []string{"analyzer", "llmvcr"}

func TestStableAPISnapshot(t *testing.T) {
	for _, name := range stablePackages {
		t.Run(name, func(t *testing.T) {
			got, err := apisnapshot.Render(t.Context(), filepath.Join(repoRoot, "pkg", name), modulePath+"/pkg/"+name)
			if err != nil {
				t.Fatalf("Render(%q) error = %v", name, err)
			}
			path := filepath.Join(repoRoot, snapshotDir, name+".txt")
			if *update {
				if err := os.WriteFile(path, []byte(got), permFile); err != nil {
					t.Fatalf("writing %s: %v", path, err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s (run make api): %v", path, err)
			}
			// A Windows checkout may convert the snapshot to CRLF.
			if diff := cmp.Diff(strings.ReplaceAll(string(want), "\r\n", "\n"), got); diff != "" {
				t.Errorf("exported API of pkg/%s changed; if intended, run make api and commit %s (-want +got):\n%s", name, path, diff)
			}
		})
	}
}

func TestRender(t *testing.T) {
	tests := []struct {
		name    string
		dir     string            // fixture under testdata
		files   map[string]string // or files written to a temporary directory
		want    string
		wantErr error
	}{
		{
			name: "exported API with used imports",
			dir:  "imports",
			want: `package imports

import (
	rand "math/rand/v2"
	str "strings"
)

const Limit = 3

var Max = defaults.max

func Pick(r *rand.Rand) *str.Builder

type Exported struct {
	Name string
	// contains filtered or unexported fields
}
`,
		},
		{name: "missing directory", dir: "missing", wantErr: fs.ErrNotExist},
		{
			name:    "malformed source",
			files:   map[string]string{"bad.go": "package bad\n\nfunc Broken( {\n"},
			wantErr: apisnapshot.ErrParse,
		},
		{
			name:    "conflicting packages",
			files:   map[string]string{"a.go": "package one\n", "b.go": "package two\n"},
			wantErr: apisnapshot.ErrPackageMismatch,
		},
		{
			name:    "only test files",
			files:   map[string]string{"only_test.go": "package only\n"},
			wantErr: apisnapshot.ErrNoGoFiles,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join("testdata", tc.dir)
			if tc.files != nil {
				dir = writeFiles(t, tc.files)
			}
			got, err := apisnapshot.Render(t.Context(), dir, "example.com/fixture")
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Render(%q) error mismatch (-want +got):\n%s", tc.name, diff)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Render(%q) mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), permFile); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return dir
}
