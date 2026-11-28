package apisnapshot_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

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
			got, err := apisnapshot.Render(filepath.Join(repoRoot, "pkg", name), modulePath+"/pkg/"+name)
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
