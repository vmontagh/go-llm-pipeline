//go:build unix

package sqlitestore_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore/sqlitestore"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore/vectorstoretest"
)

// approxScore bounds the floating-point slack allowed when comparing cosine
// similarity scores across independent normalization.
const approxScore = 1e-9

// ownerOnlyPerm is the permission bitmask Open must apply to the backing file.
const ownerOnlyPerm os.FileMode = 0o600

// Store satisfies the conformance harness interface. The assertion lives in the
// test package because the interface now belongs to vectorstoretest (its
// consumer), which production code must not import.
var _ vectorstoretest.Store = (*sqlitestore.Store)(nil)

// TestConformance runs the shared Store behavioral contract against a fresh
// SQLite-backed store rooted in a temp directory.
func TestConformance(t *testing.T) {
	t.Parallel()

	vectorstoretest.RunStoreConformance(t, func(tb testing.TB) vectorstoretest.Store {
		s, err := sqlitestore.Open(tb.Context(), filepath.Join(tb.TempDir(), "store.db"))
		if err != nil {
			tb.Fatalf("Open() = %v", err)
		}
		tb.Cleanup(func() {
			if err := s.Close(); err != nil {
				tb.Errorf("Close() = %v, want nil", err)
			}
		})
		return s
	})
}

// openStore opens a store at path, failing the test on error and closing it on
// cleanup.
func openStore(tb testing.TB, path string) *sqlitestore.Store {
	tb.Helper()
	s, err := sqlitestore.Open(tb.Context(), path)
	if err != nil {
		tb.Fatalf("Open(%q) = %v, want nil", path, err)
	}
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Errorf("Close() = %v, want nil", err)
		}
	})
	return s
}

// addItems adds each item to s in order, failing the test on the first error, so
// a test can arrange its fixture data in one readable line.
func addItems(tb testing.TB, s *sqlitestore.Store, items ...vectorstore.Item) {
	tb.Helper()
	for _, it := range items {
		if err := s.Add(tb.Context(), it); err != nil {
			tb.Fatalf("Add(%q) = %v, want nil", it.ID, err)
		}
	}
}

// runConcurrently runs each fn in its own goroutine, released together by a
// shared start signal so they contend as simultaneously as possible, and
// returns once all have finished.
func runConcurrently(fns ...func()) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn()
		}()
	}
	close(start)
	wg.Wait()
}

// runConcurrentErrs runs fn(0..n-1) each in its own goroutine and returns their
// errors by index, so a test can assert on the outcome of every racing worker.
func runConcurrentErrs(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fn(i)
		}()
	}
	wg.Wait()
	return errs
}

// TestPersistenceRoundTrip is the key backend-specific test: items added before
// a close are returned identically after reopening the same database file.
func TestPersistenceRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "store.db")
	items := []vectorstore.Item{
		{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "v"}},
		{ID: "b", Vector: vectorstore.Vector{0, 1}},
		{ID: "c", Vector: vectorstore.Vector{1, 1}, Metadata: map[string]string{"n": "3"}},
	}
	query := vectorstore.Vector{1, 0}

	first := openStore(t, path)
	addItems(t, first, items...)
	want, err := first.Nearest(t.Context(), query, len(items))
	if err != nil {
		t.Fatalf("Nearest(%v, %d) = %v, want nil", query, len(items), err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	second := openStore(t, path)
	got, err := second.Nearest(t.Context(), query, len(items))
	if err != nil {
		t.Fatalf("Nearest(%v, %d) after reopen = %v, want nil", query, len(items), err)
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateApprox(0, approxScore)); diff != "" {
		t.Errorf("neighbors after reopen mismatch (-want +got):\n%s", diff)
	}
}

// TestDimensionSurvivesRestart checks that the store's fixed dimension is
// persisted, so a mismatched Add after reopening is rejected.
func TestDimensionSurvivesRestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "store.db")
	first := openStore(t, path)
	addItems(t, first, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	second := openStore(t, path)
	err := second.Add(t.Context(), vectorstore.Item{ID: "b", Vector: vectorstore.Vector{1, 0, 0}})
	if diff := cmp.Diff(vectorstore.ErrDimensionMismatch, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Add(wrong dimension) after reopen error mismatch (-want +got):\n%s", diff)
	}
}

// TestUpsertSurvivesRestart checks that upserting an existing ID after a restart
// replaces the row rather than inserting a duplicate.
func TestUpsertSurvivesRestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "store.db")
	query := vectorstore.Vector{0, 1}

	first := openStore(t, path)
	addItems(t, first, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	second := openStore(t, path)
	want := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"v": "2"}}
	addItems(t, second, want)

	third := openStore(t, path)
	got, err := third.Nearest(t.Context(), query, 100)
	if err != nil {
		t.Fatalf("Nearest(%v, %d) = %v, want nil", query, 100, err)
	}
	if len(got) != 1 {
		t.Fatalf("item count after upsert and reopen = %d, want 1", len(got))
	}
	if diff := cmp.Diff(want, got[0].Item); diff != "" {
		t.Errorf("upserted item after reopen mismatch (-want +got):\n%s", diff)
	}
}

// assertOwnerOnly fails the test unless the file at path is restricted to
// owner-only permissions.
func assertOwnerOnly(tb testing.TB, path string) {
	tb.Helper()
	info, err := os.Stat(path)
	if err != nil {
		tb.Fatalf("Stat(%q) = %v, want nil", path, err)
	}
	if got := info.Mode().Perm(); got != ownerOnlyPerm {
		tb.Errorf("file mode of %q = %o, want %o", path, got, ownerOnlyPerm)
	}
}

// TestOpenSecuresFileModes checks that Open hardens the backing files to 0600.
// The modernc driver creates the main file world-readable by default, and under
// WAL journaling the -wal and -shm siblings can hold recent pages, so leaving
// them world-readable would leak data even with the main file locked down.
func TestOpenSecuresFileModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		write          bool     // Add an item to force the -wal/-shm siblings
		assertSuffixes []string // path suffixes to check ("" is the main file)
	}{
		{name: "main file", assertSuffixes: []string{""}},
		{name: "wal and shm siblings", write: true, assertSuffixes: []string{"-wal", "-shm"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "store.db")
			s := openStore(t, path)
			if tt.write {
				// A write forces SQLite to create the -wal and -shm siblings; the
				// store is kept open so a clean Close cannot checkpoint and remove
				// them first.
				if err := s.Add(t.Context(), vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}}); err != nil {
					t.Fatalf("Add() = %v, want nil", err)
				}
			}
			for _, suffix := range tt.assertSuffixes {
				assertOwnerOnly(t, path+suffix)
			}
		})
	}
}

// TestOpenSecuresRelativePathFileMode proves the hardening also covers a
// relative path, which resolves against the working directory. It cannot run in
// parallel because it changes the working directory.
func TestOpenSecuresRelativePathFileMode(t *testing.T) {
	t.Chdir(t.TempDir())
	const name = "store.db"
	_ = openStore(t, name)
	assertOwnerOnly(t, name)
}

// TestOpenResolvesSpecialCharPath proves Open treats path as a filesystem path,
// not a URI: paths containing space, '?', '#', and '%' open the exact requested
// file and round-trip an item across a reopen.
func TestOpenResolvesSpecialCharPath(t *testing.T) {
	t.Parallel()

	names := []string{"sp ace.db", "weird?x=1.db", "hash#a.db", "pct%20.db"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), name)
			item := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "v"}}

			first := openStore(t, path)
			if err := first.Add(t.Context(), item); err != nil {
				t.Fatalf("Add() = %v, want nil", err)
			}
			if err := first.Close(); err != nil {
				t.Fatalf("Close() = %v, want nil", err)
			}
			// The file must exist at exactly the requested path, proving SQLite
			// did not truncate the name at '?' or '#' or decode '%20'.
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("Stat(%q) after Open = %v; a different file was opened", path, err)
			}

			second := openStore(t, path)
			got, err := second.Nearest(t.Context(), vectorstore.Vector{1, 0}, 1)
			if err != nil {
				t.Fatalf("Nearest() = %v, want nil", err)
			}
			want := []vectorstore.Neighbor{{Item: item, Score: 1}}
			if diff := cmp.Diff(want, got, cmpopts.EquateApprox(0, approxScore)); diff != "" {
				t.Errorf("round-trip through %q mismatch (-want +got):\n%s", name, diff)
			}
		})
	}
}

// TestReopenSecuresWALSiblings covers the lifecycle gap the Open-time hardening
// misses: reopening an already-migrated database performs no writes, so the WAL
// sidecars are absent when Open runs and SQLite recreates them world-readable on
// the first Add. The store must harden them lazily after that write.
func TestReopenSecuresWALSiblings(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "store.db")
	item := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}}

	first, err := sqlitestore.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open(%q) = %v, want nil", path, err)
	}
	if err := first.Add(t.Context(), item); err != nil {
		t.Fatalf("Add() = %v, want nil", err)
	}
	if err := first.Close(); err != nil { // clean close checkpoints away the sidecars
		t.Fatalf("Close() = %v, want nil", err)
	}

	second := openStore(t, path)                          // reopen: migrate writes nothing, sidecars absent
	if err := second.Add(t.Context(), item); err != nil { // first write recreates them
		t.Fatalf("Add() after reopen = %v, want nil", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		assertOwnerOnly(t, path+suffix)
	}
}

// TestOpenMemoryPathIsLiteralFile proves ":memory:" is treated as a literal
// filename, not SQLite's private per-connection in-memory database: the data is
// written to a real file and survives a reopen. It cannot run in parallel
// because it changes the working directory.
func TestOpenMemoryPathIsLiteralFile(t *testing.T) {
	t.Chdir(t.TempDir())
	item := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}}

	first, err := sqlitestore.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) = %v, want nil", err)
	}
	if err := first.Add(t.Context(), item); err != nil {
		t.Fatalf("Add() = %v, want nil", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if _, err := os.Stat(":memory:"); err != nil {
		t.Fatalf("Stat(:memory:) = %v, want a real file", err)
	}

	second := openStore(t, ":memory:")
	got, err := second.Nearest(t.Context(), vectorstore.Vector{1, 0}, 1)
	if err != nil {
		t.Fatalf("Nearest() = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Item.ID != "a" {
		t.Errorf("Nearest() = %v, want the persisted item a", got)
	}
}

// where multiple writers reach the empty meta table at once. Immediate write
// locking plus an idempotent claim-then-verify must make same-dimension writers
// all succeed and different-dimension writers fail cleanly with
// ErrDimensionMismatch rather than a raw lock or constraint error. Run under
// -race.
func TestConcurrentFirstAdd(t *testing.T) {
	t.Parallel()

	t.Run("same dimension all succeed", func(t *testing.T) {
		t.Parallel()

		s := openStore(t, filepath.Join(t.TempDir(), "store.db"))
		const n = 8
		errs := runConcurrentErrs(n, func(i int) error {
			return s.Add(t.Context(), vectorstore.Item{ID: strconv.Itoa(i), Vector: vectorstore.Vector{1, 0}})
		})

		for i, err := range errs {
			if err != nil {
				t.Errorf("Add(id=%d) = %v, want nil", i, err)
			}
		}
		got, err := s.Nearest(t.Context(), vectorstore.Vector{1, 0}, n+1)
		if err != nil {
			t.Fatalf("Nearest() = %v, want nil", err)
		}
		if len(got) != n {
			t.Errorf("Nearest() returned %d neighbors, want %d", len(got), n)
		}
	})

	t.Run("different dimensions fail cleanly", func(t *testing.T) {
		t.Parallel()

		s := openStore(t, filepath.Join(t.TempDir(), "store.db"))
		const n = 8
		errs := runConcurrentErrs(n, func(i int) error {
			// Alternate two dimensions; exactly one wins the claim and the
			// losers of the other dimension must see ErrDimensionMismatch.
			vec := vectorstore.Vector{1, 0}
			if i%2 == 1 {
				vec = vectorstore.Vector{1, 0, 0}
			}
			return s.Add(t.Context(), vectorstore.Item{ID: strconv.Itoa(i), Vector: vec})
		})

		succeeded, mismatched := 0, 0
		for i, err := range errs {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, vectorstore.ErrDimensionMismatch):
				mismatched++
			default:
				t.Errorf("Add(id=%d) = %v, want nil or errors.Is(ErrDimensionMismatch)", i, err)
			}
		}
		// Exactly one dimension wins the claim: its n/2 Adds succeed and the
		// other dimension's n/2 Adds each see a clean ErrDimensionMismatch.
		if succeeded != n/2 {
			t.Errorf("succeeded = %d, want %d", succeeded, n/2)
		}
		if mismatched != n/2 {
			t.Errorf("mismatched = %d, want %d", mismatched, n/2)
		}
	})
}

func TestConcurrentFirstAddNearestNoCorruptLeak(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const iterations = 500
	dir := t.TempDir()
	item := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0, 0}} // dim-3 first Add
	query := vectorstore.Vector{1, 0}                                      // dim-2 query
	for i := range iterations {
		s := openStore(t, filepath.Join(dir, "store-"+strconv.Itoa(i)+".db"))
		var addErr, nearestErr error
		runConcurrently(
			func() {
				// dim-3 first Add: if it lands between the dimension read and the
				// row scan, a non-snapshot Nearest scores a dim-3 row against a
				// dim-2 query.
				addErr = s.Add(ctx, item)
			},
			func() {
				_, nearestErr = s.Nearest(ctx, query, 5)
			},
		)
		if addErr != nil {
			t.Fatalf("iteration %d: Add = %v, want nil", i, addErr)
		}
		// The single first Add either commits before the read snapshot starts
		// (Nearest sees the dim-3 row and returns ErrDimensionMismatch) or after
		// (Nearest sees an empty snapshot and returns nil). Any other error —
		// including ErrCorruptVector or a raw lock/schema/IO error — is a bug.
		if nearestErr != nil && !errors.Is(nearestErr, vectorstore.ErrDimensionMismatch) {
			t.Fatalf("iteration %d: Nearest = %v, want nil or errors.Is(ErrDimensionMismatch)", i, nearestErr)
		}
		// Close each store before the next iteration so 500 iterations don't hold
		// hundreds of DB/WAL/SHM descriptors at once; openStore's cleanup remains
		// as the failure-path fallback (Close is idempotent).
		if err := s.Close(); err != nil {
			t.Fatalf("iteration %d: Close = %v, want nil", i, err)
		}
	}
}
