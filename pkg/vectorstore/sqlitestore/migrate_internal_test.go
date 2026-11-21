//go:build unix

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// errStepMustNotRun is returned by a migration step that must never execute, so
// applyMigration skipping the step is observable as a nil (not this) error.
var errStepMustNotRun = errors.New("step must not run")

const (
	// permOwnerOnlyDir is a directory readable, writable, and executable only by
	// its owner.
	permOwnerOnlyDir os.FileMode = 0o700
	// permWorldWritableDir is a directory writable by group and other (no sticky
	// bit), the unsafe case Open must reject on a store-path component.
	permWorldWritableDir os.FileMode = 0o777
	// permStandardDir is a directory writable only by its owner but readable and
	// searchable by everyone.
	permStandardDir os.FileMode = 0o755
	// permGroupWritableDir is a directory writable by its group.
	permGroupWritableDir os.FileMode = 0o770
	// permWorldReadableFile is a file readable by everyone, the permissive
	// leftover Open must tighten.
	permWorldReadableFile os.FileMode = 0o644
	// otherUID is a UID that is neither the caller nor root.
	otherUID = 1234
	// nonRegularRejectTimeout bounds how long precreateSecure may take to reject a
	// non-regular path before the test treats it as blocked.
	nonRegularRejectTimeout = 2 * time.Second
)

// openStoreAt opens a Store at path for white-box tests and registers cleanup,
// failing the test if Open or Close errors.
func openStoreAt(tb testing.TB, path string) *Store {
	tb.Helper()
	s, err := Open(tb.Context(), path)
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

// openInternal opens a Store at a fresh temp path for white-box tests and
// registers cleanup, failing the test if Open or Close errors.
func openInternal(tb testing.TB) *Store {
	tb.Helper()
	return openStoreAt(tb, filepath.Join(tb.TempDir(), "store.db"))
}

// setUserVersion writes the SQLite user_version on the file at path through a
// throwaway connection, simulating a database left behind by another build.
func setUserVersion(tb testing.TB, path string, version int) {
	tb.Helper()
	db, err := sql.Open("sqlite", buildDSN(path, defaultBusyTimeout))
	if err != nil {
		tb.Fatalf("sql.Open(%q) = %v, want nil", path, err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			tb.Errorf("Close() = %v, want nil", err)
		}
	}()
	if _, err := db.ExecContext(tb.Context(), "PRAGMA user_version = "+strconv.Itoa(version)); err != nil {
		tb.Fatalf("setting user_version = %v, want nil", err)
	}
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

// injectCorruptRow writes a raw items row bypassing Add's validation, to
// simulate corrupt persisted data no legitimate Add could have written.
func injectCorruptRow(tb testing.TB, s *Store, id string, vec vectorstore.Vector) {
	tb.Helper()
	if _, err := s.db.ExecContext(tb.Context(), `INSERT INTO items (id, vector, metadata) VALUES (?, ?, NULL)`, id, encodeVector(vec)); err != nil {
		tb.Fatalf("injectCorruptRow(%q) = %v, want nil", id, err)
	}
}

func TestMigrateSetsCurrentVersion(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s := openInternal(t)

	got, err := userVersion(ctx, s.db)
	if err != nil {
		t.Fatalf("userVersion() = %v, want nil", err)
	}
	if diff := cmp.Diff(len(schemaMigrations), got); diff != "" {
		t.Errorf("user_version mismatch (-want +got):\n%s", diff)
	}
}

func TestMigrateIdempotentReopen(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "store.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() first = %v, want nil", err)
	}
	if err := first.Add(ctx, vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}}); err != nil {
		t.Fatalf("Add() = %v, want nil", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	second := openStoreAt(t, path)
	got, err := userVersion(ctx, second.db)
	if err != nil {
		t.Fatalf("userVersion() after reopen = %v, want nil", err)
	}
	if diff := cmp.Diff(len(schemaMigrations), got); diff != "" {
		t.Errorf("user_version after reopen mismatch (-want +got):\n%s", diff)
	}
	// The schema is still usable after an idempotent reopen.
	if _, err := second.Nearest(ctx, vectorstore.Vector{1, 0}, 1); err != nil {
		t.Errorf("Nearest() after reopen = %v, want nil", err)
	}
}

func TestMigrateRefusesInvalidVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version int
		wantErr error
	}{
		{name: "newer than supported", version: len(schemaMigrations) + 9999, wantErr: errSchemaTooNew},
		{name: "negative", version: -1, wantErr: errSchemaVersionInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "store.db")
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("Open() = %v, want nil", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v, want nil", err)
			}

			setUserVersion(t, path, tt.version)

			_, err = Open(ctx, path)
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Open() with user_version %d error mismatch (-want +got):\n%s", tt.version, diff)
			}
		})
	}
}

// TestBusyTimeoutSurvivesReconnect guards against a regression from the
// per-connection _pragma DSN to a one-time PRAGMA: dropping every pooled
// connection must not lose the configured busy_timeout.
func TestBusyTimeoutSurvivesReconnect(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	const busyTimeout = 5 * time.Second
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(ctx, path, WithBusyTimeout(busyTimeout))
	if err != nil {
		t.Fatalf("Open() = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() = %v, want nil", err)
		}
	})

	readBusyTimeout := func() int {
		var ms int
		if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&ms); err != nil {
			t.Fatalf("PRAGMA busy_timeout = %v, want nil", err)
		}
		return ms
	}

	wantMillis := int(busyTimeout.Milliseconds())
	if diff := cmp.Diff(wantMillis, readBusyTimeout()); diff != "" {
		t.Fatalf("busy_timeout before reconnect mismatch (-want +got):\n%s", diff)
	}
	// Force database/sql to close the pooled connection so the next query must
	// open a fresh physical connection, exercising the reconnect path.
	s.db.SetConnMaxLifetime(time.Nanosecond)
	s.db.SetMaxIdleConns(0)
	if diff := cmp.Diff(wantMillis, readBusyTimeout()); diff != "" {
		t.Errorf("busy_timeout after reconnect mismatch (-want +got):\n%s", diff)
	}
}

// TestBusyTimeoutMillis verifies conversion to whole milliseconds rounds a
// positive sub-millisecond duration up to 1ms so a configured busy timeout is
// never silently disabled by truncation.
func TestBusyTimeoutMillis(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
		want int64
	}{
		{name: "whole seconds", d: 5 * time.Second, want: 5000},
		{name: "one millisecond", d: time.Millisecond, want: 1},
		{name: "half millisecond rounds up", d: 500 * time.Microsecond, want: 1},
		{name: "one nanosecond rounds up", d: time.Nanosecond, want: 1},
		{name: "one and a half milliseconds rounds up", d: 1500 * time.Microsecond, want: 2},
		{name: "just below ceiling passes through", d: (maxBusyTimeoutMillis - 1) * time.Millisecond, want: maxBusyTimeoutMillis - 1},
		{name: "exact ceiling passes through", d: maxBusyTimeoutMillis * time.Millisecond, want: maxBusyTimeoutMillis},
		{name: "one past ceiling clamps", d: (maxBusyTimeoutMillis + 1) * time.Millisecond, want: maxBusyTimeoutMillis},
		{name: "max int64 clamps to ceiling", d: math.MaxInt64, want: maxBusyTimeoutMillis},
		{name: "zero", d: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tt.want, busyTimeoutMillis(tt.d)); diff != "" {
				t.Errorf("busyTimeoutMillis(%v) mismatch (-want +got):\n%s", tt.d, diff)
			}
		})
	}
}

func TestEffectiveBusyTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    time.Duration
		want time.Duration
	}{
		{name: "normal passes through", d: 5 * time.Second, want: 5 * time.Second},
		{name: "sub-millisecond rounds up", d: 500 * time.Microsecond, want: time.Millisecond},
		{name: "above ceiling clamps", d: math.MaxInt64, want: maxBusyTimeoutMillis * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tt.want, effectiveBusyTimeout(tt.d)); diff != "" {
				t.Errorf("effectiveBusyTimeout(%v) mismatch (-want +got):\n%s", tt.d, diff)
			}
		})
	}
}

// TestOpenRejectsEmptyPath verifies Open rejects an empty path rather than
// letting modernc interpret it as a private in-memory database.
func TestOpenRejectsEmptyPath(t *testing.T) {
	t.Parallel()

	_, err := Open(t.Context(), "")
	if diff := cmp.Diff(errEmptyPath, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Open(%q) error mismatch (-want +got):\n%s", "", diff)
	}
}

// TestOpenHardensFileBeforeFailedMigration proves the backing file is restricted
// to owner-only before migration runs, so a migration that fails cannot leave a
// world-readable file behind.
func TestOpenHardensFileBeforeFailedMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "store.db")
	// Seed a database written by a newer build so migrate refuses it, and loosen
	// perms to simulate a world-readable leftover.
	setUserVersion(t, path, len(schemaMigrations)+1)
	if err := os.Chmod(path, permWorldReadableFile); err != nil {
		t.Fatalf("Chmod = %v, want nil", err)
	}
	_, err := Open(ctx, path)
	if diff := cmp.Diff(errSchemaTooNew, err, cmpopts.EquateErrors()); diff != "" {
		t.Fatalf("Open() error mismatch (-want +got):\n%s", diff)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat = %v, want nil", err)
	}
	if diff := cmp.Diff(os.FileMode(ownerOnlyFileMode), info.Mode().Perm()); diff != "" {
		t.Errorf("mode after failed migration mismatch (-want +got):\n%s", diff)
	}
}

func TestApplyMigrationVersionGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		userVersion int
		wantErr     error
	}{
		{name: "already applied skips step", userVersion: len(schemaMigrations)},
		{name: "newer than supported refused", userVersion: len(schemaMigrations) + 1, wantErr: errSchemaTooNew},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			s := openInternal(t)
			// Simulate the on-disk ledger: at len(schemaMigrations) the step is
			// already applied; past it a concurrent newer build advanced
			// user_version after migrate()'s pre-transaction check.
			if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(tt.userVersion)); err != nil {
				t.Fatalf("set user_version = %v, want nil", err)
			}
			ran := false
			step := func(context.Context, *sql.Tx) error {
				ran = true
				return errStepMustNotRun
			}
			err := applyMigration(ctx, s.db, step, len(schemaMigrations))
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("applyMigration(user_version=%d) error mismatch (-want +got):\n%s", tt.userVersion, diff)
			}
			if ran {
				t.Errorf("migration step ran, want skipped for user_version %d", tt.userVersion)
			}
		})
	}
}

func TestConcurrentOpenMigrates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "store.db")
	const n = 8
	stores := make([]*Store, n)
	errs := runConcurrentErrs(n, func(i int) error {
		s, err := Open(ctx, path)
		stores[i] = s
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("Open(#%d) = %v, want nil", i, err)
			continue
		}
		t.Cleanup(func() {
			if cerr := stores[i].Close(); cerr != nil {
				t.Errorf("Close(#%d) = %v, want nil", i, cerr)
			}
		})
	}
	// Any successfully opened store must be at the final schema version.
	for i, s := range stores {
		if s == nil {
			continue
		}
		got, err := userVersion(ctx, s.db)
		if err != nil {
			t.Errorf("userVersion(#%d) = %v, want nil", i, err)
		}
		if got != len(schemaMigrations) {
			t.Errorf("userVersion(#%d) = %d, want %d", i, got, len(schemaMigrations))
		}
	}
}

func TestNearestCorruptStoredVector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		stored      vectorstore.Vector
		mustNotLeak error
	}{
		// Zero magnitude: decode succeeds but Normalize rejects it — corrupt
		// persisted data Add could never write. Must surface ErrCorruptVector,
		// not leak ErrInvalidVector as a caller-query fault.
		{name: "zero magnitude", stored: vectorstore.Vector{0, 0}, mustNotLeak: vectorstore.ErrInvalidVector},
		// Wrong dimension: decodes and normalizes cleanly but its dimension
		// differs from the store's (2-D). Add enforces the store dimension, so
		// this is corrupt persisted data; must surface ErrCorruptVector, not leak
		// ErrDimensionMismatch as a caller-query fault.
		{name: "wrong dimension", stored: vectorstore.Vector{1, 0, 0}, mustNotLeak: vectorstore.ErrDimensionMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			s := openInternal(t)
			if err := s.Add(ctx, vectorstore.Item{ID: "ok", Vector: vectorstore.Vector{1, 0}}); err != nil {
				t.Fatalf("Add(ok) = %v, want nil", err)
			}
			injectCorruptRow(t, s, "corrupt", tt.stored)
			_, err := s.Nearest(ctx, vectorstore.Vector{1, 0}, 5)
			// Positive: the decode failure must surface as ErrCorruptVector.
			if diff := cmp.Diff(ErrCorruptVector, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Nearest() error mismatch (-want +got):\n%s", diff)
			}
			// Negative: it must NOT leak the underlying validation sentinel, so
			// this stays an errors.Is "must-not-be" check rather than a diff.
			if errors.Is(err, tt.mustNotLeak) {
				t.Errorf("Nearest() leaked %v for corrupt data: %v", tt.mustNotLeak, err)
			}
		})
	}
}

func TestMetadataCodecRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   map[string]string
	}{
		{name: "nil map", in: nil},
		{name: "empty non-nil map", in: map[string]string{}},
		{name: "ascii", in: map[string]string{"k": "v"}},
		{name: "multi entry", in: map[string]string{"a": "1", "b": "2"}},
		{name: "invalid utf8 key and value", in: map[string]string{"k": "\xff\xfe", "\xff": "v"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeMetadata(encodeMetadata(tt.in))
			if err != nil {
				t.Fatalf("decodeMetadata(encodeMetadata(%q)) error = %v, want nil", tt.in, err)
			}
			if diff := cmp.Diff(tt.in, got); diff != "" {
				t.Errorf("metadata round-trip mismatch for %q (-want +got):\n%s", tt.in, diff)
			}
		})
	}
}

func TestDecodeMetadataRejectsCorruptBlob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		blob []byte
	}{
		{name: "empty non-nil blob", blob: []byte{}},
		{name: "truncated field", blob: []byte{0x01, 0x05, 'a', 'b'}}, // count=1, key len=5 but only 2 bytes present
		{name: "trailing bytes", blob: append(encodeMetadata(map[string]string{"k": "v"}), 0xff)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeMetadata(tt.blob)
			if diff := cmp.Diff(ErrCorruptMetadata, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("decodeMetadata(%v) error mismatch (-want +got):\n%s", tt.blob, diff)
			}
		})
	}
}

// cancelAfterQuery runs queries against q and cancels the scan's context as
// soon as a query has started, so the cancellation lands mid-scan with rows
// still open.
type cancelAfterQuery struct {
	q      rowQuerier
	cancel context.CancelFunc
}

func (c cancelAfterQuery) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := c.q.QueryContext(ctx, query, args...)
	c.cancel()
	return rows, err
}

func TestScanItemsCanceledMidScan(t *testing.T) {
	t.Parallel()
	s := openInternal(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := s.Add(t.Context(), vectorstore.Item{ID: id, Vector: vectorstore.Vector{1, 0}}); err != nil {
			t.Fatalf("Add(%q) = %v, want nil", id, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	got, err := scanItems(ctx, cancelAfterQuery{q: s.db, cancel: cancel}, vectorstore.Vector{1, 0})

	if diff := cmp.Diff(context.Canceled, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("scanItems() error mismatch (-want +got):\n%s", diff)
	}
	if len(got) != 0 {
		t.Errorf("scanItems() = %d partial neighbors, want none", len(got))
	}
}

// TestNearestReleasesReadSnapshotOnError proves a Nearest that fails after
// opening its read snapshot rolls it back: no connection stays checked out and
// no reader pins the WAL, so a full checkpoint is not blocked.
func TestNearestReleasesReadSnapshotOnError(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := openInternal(t)
	if err := s.Add(ctx, vectorstore.Item{ID: "ok", Vector: vectorstore.Vector{1, 0}}); err != nil {
		t.Fatalf("Add(ok) = %v, want nil", err)
	}
	injectCorruptRow(t, s, "corrupt", vectorstore.Vector{0, 0})

	if _, err := s.Nearest(ctx, vectorstore.Vector{1, 0}, 1); !errors.Is(err, ErrCorruptVector) {
		t.Fatalf("Nearest() = %v, want ErrCorruptVector to fail mid-scan", err)
	}

	if diff := cmp.Diff(0, s.db.Stats().InUse); diff != "" {
		t.Errorf("connections in use after failed Nearest mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(0, checkpointBusy(t, s)); diff != "" {
		t.Errorf("wal_checkpoint(TRUNCATE) busy mismatch (-want +got):\n%s", diff)
	}
}

// checkpointBusy runs a truncating WAL checkpoint and returns SQLite's busy
// flag, which is 1 when an open reader prevented the checkpoint from finishing.
func checkpointBusy(tb testing.TB, s *Store) int {
	tb.Helper()
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(tb.Context(), "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		tb.Fatalf("PRAGMA wal_checkpoint(TRUNCATE) = %v, want nil", err)
	}
	return busy
}

// TestPrecreateSecureRejectsNonRegularFile proves a path that is not a regular
// file is rejected promptly (a FIFO open must not block waiting for a writer)
// and left untouched.
func TestPrecreateSecureRejectsNonRegularFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{name: "directory", setup: func(t *testing.T) string {
			t.Helper()
			return t.TempDir()
		}},
		{name: "fifo", setup: func(t *testing.T) string {
			t.Helper()
			fifo := filepath.Join(t.TempDir(), "fifo.db")
			if err := syscall.Mkfifo(fifo, uint32(ownerOnlyFileMode)); err != nil {
				t.Fatalf("Mkfifo(%q) = %v, want nil", fifo, err)
			}
			return fifo
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := tt.setup(t)
			before := statMode(t, path)

			err := precreateSecureWithin(t, path, nonRegularRejectTimeout)

			if diff := cmp.Diff(errNotRegularFile, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("precreateSecure(%s) error mismatch (-want +got):\n%s", tt.name, diff)
			}
			if diff := cmp.Diff(before, statMode(t, path)); diff != "" {
				t.Errorf("precreateSecure(%s) changed mode (-before +after):\n%s", tt.name, diff)
			}
		})
	}
}

// statMode returns the mode of path, failing the test if it cannot be read.
func statMode(tb testing.TB, path string) os.FileMode {
	tb.Helper()
	info, err := os.Stat(path)
	if err != nil {
		tb.Fatalf("Stat(%q) = %v, want nil", path, err)
	}
	return info.Mode()
}

// precreateSecureWithin runs precreateSecure and fails the test if it does not
// return within timeout, so a blocking open is reported instead of hanging.
func precreateSecureWithin(t *testing.T, path string, timeout time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- precreateSecure(path) }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("precreateSecure(%q) blocked for %v; want a prompt rejection", path, timeout)
		return nil
	}
}

func TestDirComponentSafe(t *testing.T) {
	t.Parallel()

	const caller = 1000
	tests := []struct {
		name      string
		immediate bool
		mode      os.FileMode
		owner     int
		wantErr   error
	}{
		{name: "immediate owner-only owned by caller", immediate: true, mode: permOwnerOnlyDir, owner: caller},
		{name: "immediate owner-only owned by root", immediate: true, mode: permOwnerOnlyDir, owner: rootUID},
		{name: "immediate owned by other user", immediate: true, mode: permStandardDir, owner: otherUID, wantErr: errParentDirUntrusted},
		{name: "immediate group or other writable", immediate: true, mode: permGroupWritableDir, owner: caller, wantErr: errParentDirWritable},
		{name: "immediate sticky world-writable still rejected", immediate: true, mode: os.ModeSticky | permWorldWritableDir, owner: caller, wantErr: errParentDirWritable},
		{name: "ancestor owner-only owned by caller", mode: permStandardDir, owner: caller},
		{name: "ancestor owner-only owned by root", mode: permStandardDir, owner: rootUID},
		{name: "ancestor owned by other user", mode: permStandardDir, owner: otherUID, wantErr: errParentDirUntrusted},
		{name: "ancestor world-writable no sticky", mode: permWorldWritableDir, owner: caller, wantErr: errParentDirWritable},
		{name: "ancestor world-writable with sticky", mode: os.ModeSticky | permWorldWritableDir, owner: caller},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := dirComponentSafe(tt.immediate, tt.mode, tt.owner, caller)
			if diff := cmp.Diff(tt.wantErr, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("dirComponentSafe(%t, %v, owner=%d, caller=%d) error mismatch (-want +got):\n%s", tt.immediate, tt.mode, tt.owner, caller, diff)
			}
		})
	}
}

func TestOpenDirectorySafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(t *testing.T) string // builds the dir tree, returns the db path
		wantErr error                     // nil means Open must succeed
	}{
		{
			name: "world-writable immediate dir",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := filepath.Join(t.TempDir(), "sub")
				if err := os.Mkdir(dir, permStandardDir); err != nil {
					t.Fatalf("Mkdir = %v, want nil", err)
				}
				if err := os.Chmod(dir, permWorldWritableDir); err != nil { // group/other-writable, no sticky
					t.Fatalf("Chmod = %v, want nil", err)
				}
				return filepath.Join(dir, "store.db")
			},
			wantErr: errParentDirWritable,
		},
		{
			name: "sticky world-writable immediate dir",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := filepath.Join(t.TempDir(), "sticky")
				if err := os.Mkdir(dir, permWorldWritableDir); err != nil {
					t.Fatalf("Mkdir = %v, want nil", err)
				}
				if err := os.Chmod(dir, os.ModeSticky|permWorldWritableDir); err != nil {
					t.Fatalf("Chmod = %v, want nil", err)
				}
				return filepath.Join(dir, "store.db")
			},
			wantErr: errParentDirWritable,
		},
		{
			name: "owner-only dir under sticky ancestor",
			setup: func(t *testing.T) string {
				t.Helper()
				sticky := filepath.Join(t.TempDir(), "sticky")
				if err := os.Mkdir(sticky, permWorldWritableDir); err != nil {
					t.Fatalf("Mkdir(sticky) = %v, want nil", err)
				}
				if err := os.Chmod(sticky, os.ModeSticky|permWorldWritableDir); err != nil {
					t.Fatalf("Chmod(sticky) = %v, want nil", err)
				}
				leaf := filepath.Join(sticky, "app") // created by us, owned by the current user
				if err := os.Mkdir(leaf, permOwnerOnlyDir); err != nil {
					t.Fatalf("Mkdir(leaf) = %v, want nil", err)
				}
				return filepath.Join(leaf, "store.db")
			},
		},
		{
			name: "owner-only dir under writable ancestor",
			setup: func(t *testing.T) string {
				t.Helper()
				shared := filepath.Join(t.TempDir(), "shared")
				if err := os.Mkdir(shared, permWorldWritableDir); err != nil {
					t.Fatalf("Mkdir(shared) = %v, want nil", err)
				}
				if err := os.Chmod(shared, permWorldWritableDir); err != nil { // group/other-writable, no sticky (defeat umask)
					t.Fatalf("Chmod(shared) = %v, want nil", err)
				}
				user := filepath.Join(shared, "user")
				if err := os.Mkdir(user, permOwnerOnlyDir); err != nil { // owner-only leaf, but swappable via shared
					t.Fatalf("Mkdir(user) = %v, want nil", err)
				}
				return filepath.Join(user, "store.db")
			},
			wantErr: errParentDirWritable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := tt.setup(t)
			s, err := Open(t.Context(), path)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Open(%s) = %v, want nil", path, err)
				}
				if err := s.Close(); err != nil {
					t.Errorf("Close() = %v, want nil", err)
				}
				return
			}
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Open(%s) error mismatch (-want +got):\n%s", path, diff)
			}
		})
	}
}

func TestOpenRejectsConflictingLegacyTables(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "legacy.db")
	setup, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open = %v, want nil", err)
	}
	if _, err := setup.ExecContext(ctx, `CREATE TABLE items (foo TEXT); CREATE TABLE meta (foo TEXT);`); err != nil {
		t.Fatalf("creating legacy tables = %v, want nil", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatalf("closing setup db = %v, want nil", err)
	}
	// The file is user_version 0 (default) with incompatibly shaped tables.
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("Open(conflicting legacy tables) = nil error, want a schema-creation error")
	}
	// Prove the file was NOT marked current.
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen for version check = %v, want nil", err)
	}
	defer func() {
		if err := check.Close(); err != nil {
			t.Errorf("closing version-check db = %v, want nil", err)
		}
	}()
	var version int
	if err := check.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("reading user_version = %v, want nil", err)
	}
	if version != 0 {
		t.Errorf("user_version after refused adoption = %d, want 0", version)
	}
}

func TestPingRetryBackoff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		remaining time.Duration
		wantDelay time.Duration
		wantRetry bool
	}{
		{name: "deadline passed", remaining: -time.Millisecond, wantRetry: false},
		{name: "deadline reached", remaining: 0, wantRetry: false},
		{name: "less than delay is capped", remaining: time.Millisecond, wantDelay: time.Millisecond, wantRetry: true},
		{name: "equal to delay", remaining: pingRetryDelay, wantDelay: pingRetryDelay, wantRetry: true},
		{name: "more than delay uses delay", remaining: 10 * pingRetryDelay, wantDelay: pingRetryDelay, wantRetry: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotDelay, gotRetry := pingRetryBackoff(tt.remaining)
			if diff := cmp.Diff(tt.wantDelay, gotDelay); diff != "" {
				t.Errorf("pingRetryBackoff(%v) delay mismatch (-want +got):\n%s", tt.remaining, diff)
			}
			if diff := cmp.Diff(tt.wantRetry, gotRetry); diff != "" {
				t.Errorf("pingRetryBackoff(%v) retry mismatch (-want +got):\n%s", tt.remaining, diff)
			}
		})
	}
}
