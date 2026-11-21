// Package sqlitestore provides a persistent, SQLite-backed vector store
// built on the pure-Go, cgo-free modernc.org/sqlite driver, so it needs no C
// toolchain and runs fully offline and local-first. Its [Store] satisfies the
// [github.com/vmontagh/go-llm-pipeline/pkg/vectorstore/vectorstoretest.Store]
// conformance interface.
//
// A store fixes its vector dimension on the first successful Add and persists it
// so the constraint survives a restart. Items are upserted by [vectorstore.Item]
// ID. Callers own the lifecycle explicitly through [Open] and [Store.Close];
// there are no finalizers.
//
// The backend reuses the parent package's normalization, scoring, and sentinel
// errors so its observable behavior matches [vectorstore.MemoryStore]. It stores
// the original caller vector rather than a normalized copy, so Nearest returns
// the exact vector the caller added.
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	// modernc.org/sqlite registers the "sqlite" database/sql driver via its
	// init; it is imported by name to detect SQLITE_BUSY on concurrent opens.
	sqlite3 "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

const (
	// metaDimKey is the meta-table key under which the store's fixed vector
	// dimension is persisted, so the constraint survives a restart.
	metaDimKey = "dimension"

	// sqliteDriverName is the database/sql driver name registered by the
	// blank-imported modernc.org/sqlite driver.
	sqliteDriverName = "sqlite"

	// pragmaUserVersion reads SQLite's user_version schema-version counter, which
	// this package uses as its migration ledger.
	pragmaUserVersion = "PRAGMA user_version"

	// rootUID is the trusted superuser UID; a directory component on the store path
	// owned by root is trusted like one owned by the caller.
	rootUID = 0

	// defaultBusyTimeout is the SQLite busy_timeout applied when the caller does not
	// override it, bounding how long a write waits on a competing lock.
	defaultBusyTimeout = 5 * time.Second

	// primaryResultCodeMask selects SQLite's primary result code from an extended
	// one: the driver enables extended result codes, so a lock error such as
	// SQLITE_BUSY_RECOVERY (261) carries SQLITE_BUSY (5) only in its low byte.
	primaryResultCodeMask = 0xff

	// pingRetryDelay bounds how long Open waits between retries while the WAL
	// conversion lock is briefly held by a competing opener.
	pingRetryDelay = 5 * time.Millisecond

	// maxBusyTimeoutMillis is the largest busy_timeout SQLite honors. SQLite
	// parses PRAGMA busy_timeout as a signed 32-bit integer, so a larger value
	// wraps to a non-positive number and silently disables lock waiting;
	// busyTimeoutMillis clamps to this ceiling (~24.8 days) instead.
	maxBusyTimeoutMillis = math.MaxInt32

	// ownerOnlyFileMode restricts the backing database file to the owner, since the
	// vectors and metadata it stores should not be world-readable.
	ownerOnlyFileMode = 0o600

	// groupOtherWritePerm is the group- and other-write permission bits; a store
	// directory carrying any of them is writable by users other than the owner.
	groupOtherWritePerm os.FileMode = 0o022
)

var (
	// walSiblingSuffixes are the WAL journaling sidecar files SQLite creates next to
	// the main database file; they are permission-hardened alongside it.
	walSiblingSuffixes = []string{"-wal", "-shm"}

	// ErrCorruptVector is returned when a stored vector blob cannot be decoded,
	// for example because its length is not a whole number of float64 values.
	ErrCorruptVector = errors.New("corrupt vector blob")

	// ErrCorruptMetadata is returned when a stored metadata blob cannot be decoded,
	// which indicates on-disk corruption or a foreign writer.
	ErrCorruptMetadata = errors.New("corrupt metadata blob")

	// errSchemaTooNew is returned when the database records a schema version newer
	// than this build understands, so it is refused rather than silently misread.
	errSchemaTooNew = errors.New("sqlite schema version is newer than supported")

	// errSchemaVersionInvalid is returned when the on-disk schema version is
	// negative, which would otherwise index the migration table out of bounds.
	errSchemaVersionInvalid = errors.New("sqlite schema version is invalid")

	// errDimensionReadBack is returned when the store dimension is missing
	// immediately after its claiming insert, which would indicate a violated
	// invariant rather than a normal condition.
	errDimensionReadBack = errors.New("dimension missing after claiming insert")

	// errEmptyPath is returned by Open when path is empty.
	errEmptyPath = errors.New("empty database path")

	// errNotRegularFile is returned when the store path resolves to a directory or
	// other non-regular file, which must not be opened or chmod-ed as a database.
	errNotRegularFile = errors.New("sqlite store path is not a regular file")

	// errParentDirWritable is returned when the store file's parent directory, or
	// an ancestor directory, is writable by group or other without the sticky bit,
	// so another user could replace the hardened database file in the gap between
	// precreation and the driver's lazy reopen and defeat the owner-only guarantee.
	errParentDirWritable = errors.New("sqlite store parent directory is writable by others")

	// errParentDirUntrusted is returned when a directory component on the store
	// path is owned by a user other than the caller or root. Such an owner can
	// change the directory's mode or replace its entries at any time, so the
	// permission-bit checks establish no guarantee; only the caller or root (a
	// trusted UID) may own a component on the path.
	errParentDirUntrusted = errors.New("sqlite store directory is owned by an untrusted user")

	// errUnexpectedSchemaVersion is returned when, while holding the migration write
	// lock, the on-disk version is neither already-applied nor the expected
	// predecessor, so applying the step could corrupt a partially migrated ledger.
	errUnexpectedSchemaVersion = errors.New("unexpected schema version")
)

// config holds the options applied by Open.
type config struct {
	busyTimeout time.Duration
}

// Option customizes a Store at construction time.
type Option func(*config)

// WithBusyTimeout sets how long SQLite waits for a lock before returning a busy
// error. Non-positive values are ignored, keeping the default timeout. SQLite's
// busy_timeout has millisecond granularity, so a positive sub-millisecond value
// is rounded up to 1ms rather than being disabled.
func WithBusyTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.busyTimeout = d
		}
	}
}

// Store is a persistent, SQLite-backed vector store. It holds every
// vector in a SQLite table and scans all rows on each query. The zero value is
// not usable; construct one with [Open].
type Store struct {
	db     *sql.DB
	dbFile string // resolved main database file; "" for an in-memory database

	mu         sync.Mutex // guards walSecured
	walSecured bool       // whether the WAL sidecars have been hardened post-write
}

// Open opens (creating if necessary) a SQLite database at path and returns a
// ready Store. path is a filesystem path, not a DSN: characters such as '?',
// '#', and '%' are treated as part of the filename, and a special string like
// ":memory:" is treated as a literal filename (an in-memory database is not
// supported). Open resolves path to an absolute path, creates the backing file
// restricted to owner-only access before opening it, applies WAL journaling and
// a busy timeout, verifies the connection with PingContext, and migrates the
// schema to the current version. The caller owns the returned Store and must
// Close it. Owner-only permissions are enforced with Unix mode bits, so Open is
// supported only on Unix platforms; on any non-Unix platform (for example
// Windows, which uses ACLs rather than Unix permission bits) Open returns an
// error rather than claiming a protection it cannot provide. Open resolves
// symlinks in the store's directory and requires every directory component on
// the path to be owned by the caller or root — a component owned by another
// user is untrusted regardless of its mode bits, since that owner can change
// its mode or replace its entries. It also rejects a store whose immediate
// directory is writable by other users, or whose ancestor is writable without
// the sticky bit, so the hardened file cannot be swapped for a substitute in
// the gap before the driver's lazy open.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	cfg := config{busyTimeout: defaultBusyTimeout}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	if path == "" {
		return nil, fmt.Errorf("opening sqlite store: %w", errEmptyPath)
	}
	if err := ensureOwnerOnlySupported(); err != nil {
		return nil, fmt.Errorf("opening sqlite store %q: %w", path, err)
	}
	// Resolve to an absolute filesystem path so a special string such as
	// ":memory:" is treated as a literal file, not SQLite's private
	// per-connection in-memory database (which, under connection pooling, leaves
	// some physical connections without the migrated schema).
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving sqlite store path %q: %w", path, err)
	}
	canonical, err := ensureCanonicalStorePath(abs)
	if err != nil {
		return nil, err
	}
	// Create and harden the file before opening it, so schema writes never pass
	// through a world-readable file and a failed migration cannot leave one.
	if err := precreateSecure(canonical); err != nil {
		return nil, err
	}

	effective := effectiveBusyTimeout(cfg.busyTimeout)
	db, err := sql.Open(sqliteDriverName, buildDSN(canonical, effective))
	if err != nil {
		return nil, fmt.Errorf("opening sqlite store %q: %w", path, err)
	}
	if err := pingWithRetry(ctx, db, effective); err != nil {
		return nil, closeOnError(db, fmt.Errorf("pinging sqlite store %q: %w", path, err))
	}
	if err := migrate(ctx, db); err != nil {
		return nil, closeOnError(db, fmt.Errorf("initializing sqlite store %q: %w", path, err))
	}
	file, err := mainDatabaseFile(ctx, db)
	if err != nil {
		return nil, closeOnError(db, fmt.Errorf("securing sqlite store %q: %w", path, err))
	}
	if err := secureFileMode(file); err != nil {
		return nil, closeOnError(db, fmt.Errorf("securing sqlite store %q: %w", path, err))
	}
	return &Store{db: db, dbFile: file}, nil
}

// pingRetryBackoff returns how long to wait before the next ping retry and
// whether to retry at all, given the time left before the busy-timeout
// deadline. It caps the wait at the remaining time so a busy_timeout shorter
// than pingRetryDelay is still honored, and reports no retry once the deadline
// has passed.
func pingRetryBackoff(remaining time.Duration) (time.Duration, bool) {
	if remaining <= 0 {
		return 0, false
	}
	if remaining < pingRetryDelay {
		return remaining, true
	}
	return pingRetryDelay, true
}

// pingWithRetry verifies the connection, retrying while the first connection's
// WAL journal-mode conversion loses a brief exclusive-lock race to a competing
// opener. SQLite reports that race as SQLITE_BUSY without honoring busy_timeout,
// but it is transient: once any opener finishes the conversion the database is
// already in WAL mode and a retry's journal_mode pragma is a no-op. Retries are
// bounded by busyTimeout so a genuinely stuck lock still surfaces an error.
func pingWithRetry(ctx context.Context, db *sql.DB, busyTimeout time.Duration) error {
	deadline := time.Now().Add(busyTimeout)
	for {
		err := db.PingContext(ctx)
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		delay, retry := pingRetryBackoff(time.Until(deadline))
		if !retry {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("pinging sqlite store: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// isSQLiteBusy reports whether err is a SQLITE_BUSY error from the driver,
// including its extended forms. Converting a fresh database to WAL journal mode
// needs a brief exclusive lock that SQLite does not retry via busy_timeout, so
// concurrent first opens can see this transiently and must retry rather than
// fail.
func isSQLiteBusy(err error) bool {
	var serr *sqlite3.Error
	return errors.As(err, &serr) && isBusyCode(serr.Code())
}

// isBusyCode reports whether an (extended) SQLite result code is SQLITE_BUSY.
func isBusyCode(code int) bool {
	return code&primaryResultCodeMask == sqlitelib.SQLITE_BUSY
}

// dirComponentSafe reports why a directory component on the resolved store path
// is unsafe against replacement by another user, or nil if it is safe. owner is
// the component's owning UID and caller is the current UID. A component owned by
// anyone other than the caller or root is untrusted (its owner controls its mode
// and entries). The immediate directory (immediate=true) additionally holds the
// database and its -wal/-shm sidecars, which SQLite creates by name on demand,
// so it must not be writable by group or other at all; an ancestor may be
// group/other-writable only if the sticky bit prevents non-owners from renaming
// or replacing its entries.
func dirComponentSafe(immediate bool, mode os.FileMode, owner, caller int) error {
	if owner != caller && owner != rootUID {
		return errParentDirUntrusted
	}
	if immediate {
		if mode.Perm()&groupOtherWritePerm != 0 {
			return errParentDirWritable
		}
		return nil
	}
	if mode.Perm()&groupOtherWritePerm != 0 && mode&os.ModeSticky == 0 {
		return errParentDirWritable
	}
	return nil
}

// ensureCanonicalStorePath resolves the store file's parent directory through
// filepath.EvalSymlinks — eliminating symlink components another user could
// retarget — and verifies that no ancestor lets another user substitute a
// component of the path between precreateSecure and the driver's lazy reopen
// (sql.Open is lazy; the file is reopened by pathname at PingContext). Every
// directory component, from the immediate directory to the filesystem root,
// must be owned by the caller or root, because a component owned by another
// user is untrusted regardless of its mode bits — that owner can chmod it or
// replace its entries at any time. In addition, the immediate directory must be
// owner-only (no sticky exception, since SQLite creates the -wal/-shm sidecars
// there by name and the sticky bit guards only existing entries from removal,
// not creation of the absent sidecar names), and every ancestor must be
// owner-only or world/group-writable with the sticky bit. It returns the
// canonical, symlink-free path the driver should open.
func ensureCanonicalStorePath(path string) (string, error) {
	dir := filepath.Dir(path)
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolving sqlite store directory %q: %w", dir, err)
	}
	caller := os.Getuid()
	immediate := true
	for cur := resolvedDir; ; {
		info, err := os.Lstat(cur)
		if err != nil {
			return "", fmt.Errorf("checking sqlite store directory %q: %w", cur, err)
		}
		owner, err := fileOwnerUID(info)
		if err != nil {
			return "", fmt.Errorf("checking ownership of %q: %w", cur, err)
		}
		if err := dirComponentSafe(immediate, info.Mode(), owner, caller); err != nil {
			return "", fmt.Errorf("%w: %q has mode %v, owner uid %d", err, cur, info.Mode(), owner)
		}
		immediate = false
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return filepath.Join(resolvedDir, filepath.Base(path)), nil
}

// precreateSecure creates path if absent and restricts it to owner-only access
// before the database is opened, so schema writes never pass through a
// world-readable file and a failed migration cannot leave a permissive file
// behind. It validates and hardens through the open handle (fstat + fchmod)
// before closing it, so a directory or other non-regular path is rejected
// rather than mangled, and a concurrently swapped path cannot redirect the
// permission change. The open is non-blocking and does not follow a
// final-component symlink, so a FIFO or other special file is rejected promptly
// by the fstat rather than blocking an uncancelable open. An existing file's
// contents are preserved (no truncation).
func precreateSecure(path string) (err error) {
	f, err := os.OpenFile(path, precreateOpenFlags, ownerOnlyFileMode)
	if errors.Is(err, syscall.EISDIR) {
		// Linux refuses a writable open of a directory before fstat can see it.
		return fmt.Errorf("opening sqlite store file %q: %w", path, errors.Join(errNotRegularFile, err))
	}
	if err != nil {
		return fmt.Errorf("creating sqlite store file %q: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing sqlite store file %q: %w", path, cerr))
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("checking sqlite store file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("opening sqlite store file %q: %w", path, errNotRegularFile)
	}
	// fchmod the exact file the handle refers to (not the pathname after close),
	// so a swapped path cannot redirect the change; O_CREATE honors umask and
	// never tightens an existing file, so chmod explicitly.
	if err := f.Chmod(ownerOnlyFileMode); err != nil {
		return fmt.Errorf("restricting file permissions on %q: %w", path, err)
	}
	return nil
}

// closeOnError closes db and joins any close failure with primary, so a cleanup
// failure during a failed Open is surfaced rather than swallowed.
func closeOnError(db *sql.DB, primary error) error {
	if cerr := db.Close(); cerr != nil {
		return errors.Join(primary, fmt.Errorf("closing sqlite store: %w", cerr))
	}
	return primary
}

// buildDSN builds a modernc sqlite DSN for a filesystem path, which Open has
// already resolved to an absolute path. The path is percent-encoded into the
// file: URI so that '?', '#', and '%' are treated as part of the filename rather
// than URI syntax; otherwise SQLite would resolve them to a different file. The
// busy-timeout and WAL pragmas follow as query parameters. _txlock=immediate
// makes every explicit transaction take its write lock at BEGIN, so concurrent
// writers serialize (bounded by busy_timeout) instead of racing into a raw
// "database is locked" error; read-only queries are unaffected.
func buildDSN(path string, busyTimeout time.Duration) string {
	escaped := (&url.URL{Path: path}).EscapedPath()
	return fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_txlock=immediate", escaped, busyTimeoutMillis(busyTimeout))
}

// busyTimeoutMillis converts d to whole milliseconds for SQLite's busy_timeout,
// rounding a positive sub-millisecond duration up to 1ms so a configured timeout
// is never silently disabled by truncation. SQLite's busy_timeout has
// millisecond granularity. It divides before rounding up so a large positive
// duration near the maximum does not overflow, and clamps to SQLite's signed
// 32-bit ceiling so an out-of-range value does not wrap and disable lock waiting.
func busyTimeoutMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	ms := d / time.Millisecond
	if d%time.Millisecond != 0 {
		ms++
	}
	if ms > maxBusyTimeoutMillis {
		return maxBusyTimeoutMillis
	}
	return int64(ms)
}

// effectiveBusyTimeout returns the busy timeout SQLite actually enforces for d:
// the clamped, millisecond-granular value encoded into the DSN. Open uses it for
// the ping retry deadline so the retry window matches SQLite's real lock wait
// rather than a larger nominal duration.
func effectiveBusyTimeout(d time.Duration) time.Duration {
	return time.Duration(busyTimeoutMillis(d)) * time.Millisecond
}

// schemaMigrations lists the ordered schema upgrade steps. The number of applied
// migrations is stored in PRAGMA user_version, so an older database is upgraded
// in place and one written by a newer build is refused rather than misread.
var schemaMigrations = []func(ctx context.Context, tx *sql.Tx) error{
	migrateInitialSchema,
}

// migrateInitialSchema is migration v0->v1: it creates the meta and items
// tables. It uses plain CREATE TABLE (no IF NOT EXISTS) so a pre-existing,
// incompatibly shaped table of the same name fails the migration and rolls back
// rather than being silently adopted and recorded as v1. This store always
// writes user_version=1 atomically with table creation, so no legitimate
// "v0-with-tables" file is ever produced here; a v0 file that already has these
// tables is foreign or corrupt and must not be adopted.
func migrateInitialSchema(ctx context.Context, tx *sql.Tx) error {
	const schema = `
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE items (
	id       TEXT PRIMARY KEY,
	vector   BLOB NOT NULL,
	metadata BLOB
);`
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating initial schema: %w", err)
	}
	return nil
}

// migrate brings the schema up to date using a PRAGMA user_version ledger. It
// refuses a negative (invalid) version and a version newer than this build
// supports, and otherwise applies each pending migration in its own transaction.
func migrate(ctx context.Context, db *sql.DB) error {
	version, err := userVersion(ctx, db)
	if err != nil {
		return err
	}
	switch {
	case version < 0:
		return fmt.Errorf("%w: on-disk version %d", errSchemaVersionInvalid, version)
	case version > len(schemaMigrations):
		return fmt.Errorf("%w: on-disk version %d, supported up to %d", errSchemaTooNew, version, len(schemaMigrations))
	}
	for i := version; i < len(schemaMigrations); i++ {
		if err := applyMigration(ctx, db, schemaMigrations[i], i+1); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration step and bumps user_version to newVersion in
// the same transaction, so a partial upgrade never commits.
func applyMigration(ctx context.Context, db *sql.DB, step func(context.Context, *sql.Tx) error, newVersion int) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning migration to v%d: %w", newVersion, err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back migration to v%d: %w", newVersion, rbErr))
		}
	}()
	// Re-read the ledger while holding the immediate write lock (_txlock=
	// immediate acquired it at BEGIN). A concurrent Open may have already
	// applied this step; only apply when the version is still the expected
	// predecessor, so a future non-idempotent migration is never applied twice.
	// It also re-checks the supported maximum under the lock, since a concurrent
	// newer process may have advanced user_version past this build after
	// migrate()'s pre-transaction check.
	var current int
	current, err = userVersionTx(ctx, tx)
	if err != nil {
		return err
	}
	if current > len(schemaMigrations) {
		return fmt.Errorf("migrating to v%d: %w: on-disk version %d, supported up to %d", newVersion, errSchemaTooNew, current, len(schemaMigrations))
	}
	if current >= newVersion {
		return nil
	}
	if current != newVersion-1 {
		return fmt.Errorf("migrating to v%d: %w: %d", newVersion, errUnexpectedSchemaVersion, current)
	}
	if err := step(ctx, tx); err != nil {
		return err
	}
	// user_version cannot be a bound parameter; newVersion is an internal loop
	// index, never caller input, so building the statement is injection-safe.
	if _, err := tx.ExecContext(ctx, pragmaUserVersion+" = "+strconv.Itoa(newVersion)); err != nil {
		return fmt.Errorf("setting schema version %d: %w", newVersion, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing migration to v%d: %w", newVersion, err)
	}
	return nil
}

// userVersion reads the SQLite PRAGMA user_version schema-version counter.
func userVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, pragmaUserVersion).Scan(&version); err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	return version, nil
}

// userVersionTx reads PRAGMA user_version within an open transaction, so a
// migration can re-check the ledger while holding the write lock.
func userVersionTx(ctx context.Context, tx *sql.Tx) (int, error) {
	var version int
	if err := tx.QueryRowContext(ctx, pragmaUserVersion).Scan(&version); err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	return version, nil
}

// mainDatabaseFile returns the absolute path of the file backing the main
// database, as resolved by SQLite itself. It is empty for an in-memory or
// temporary database, which has no backing file.
func mainDatabaseFile(ctx context.Context, db *sql.DB) (string, error) {
	const query = `SELECT file FROM pragma_database_list WHERE name = 'main'`
	var file string
	if err := db.QueryRowContext(ctx, query).Scan(&file); err != nil {
		return "", fmt.Errorf("resolving database file: %w", err)
	}
	return file, nil
}

// secureFileMode restricts file and any existing WAL sidecars to owner-only
// access. file is the resolved main database path ("" for in-memory, skipped).
func secureFileMode(file string) error {
	if file == "" {
		return nil
	}
	if err := os.Chmod(file, ownerOnlyFileMode); err != nil {
		return fmt.Errorf("restricting file permissions on %q: %w", file, err)
	}
	return hardenWALSiblings(file)
}

// hardenWALSiblings restricts the -wal and -shm sidecars to owner-only access.
// They may not exist yet (SQLite creates them on the first write), so a missing
// sibling is tolerated; any other chmod failure is surfaced.
func hardenWALSiblings(file string) error {
	for _, suffix := range walSiblingSuffixes {
		sibling := file + suffix
		if err := os.Chmod(sibling, ownerOnlyFileMode); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restricting file permissions on %q: %w", sibling, err)
		}
	}
	return nil
}

// ensureWALSiblingsSecured hardens the -wal/-shm sidecars after a successful
// write. When an existing database is reopened, migrate performs no writes, so
// the sidecars are absent when Open hardens the files and SQLite (re)creates
// them world-readable on the first write. This restricts them once, lazily,
// after that write. A mutex with retry (not sync.Once) is used because chmod is
// fallible and should be retried on a later write if it fails.
func (s *Store) ensureWALSiblingsSecured() error {
	if s.dbFile == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.walSecured {
		return nil
	}
	if err := hardenWALSiblings(s.dbFile); err != nil {
		return err
	}
	s.walSecured = true
	return nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("closing sqlite store: %w", err)
	}
	return nil
}

// Add inserts item, replacing any existing item with the same ID. The original
// caller vector and metadata are persisted verbatim; the vector is validated and
// normalized only to reject invalid input and to fix the store dimension. The
// upsert runs in a transaction so the dimension record and the row commit
// together. Add returns an error if the item was persisted but its on-disk WAL
// sidecars could not be secured.
//
// Storing the raw vector (normalizing only for validation and dimension, and
// again at query time for scoring) is required by the conformance round-trip
// contract and matches [vectorstore.MemoryStore]; it supersedes issue #36's
// original "persist normalized vectors" wording.
func (s *Store) Add(ctx context.Context, item vectorstore.Item) (err error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("adding item: %w", err)
	}
	if item.ID == "" {
		return fmt.Errorf("adding item: %w", vectorstore.ErrEmptyID)
	}
	// Normalize validates the vector (empty, non-finite, zero magnitude) using
	// the same rules as MemoryStore; the normalized result is discarded because
	// scoring happens at query time over the stored original vector.
	if _, err := vectorstore.Normalize(item.Vector); err != nil {
		return fmt.Errorf("adding item %q: %w", item.ID, err)
	}

	blob := encodeVector(item.Vector)
	metadata := encodeMetadata(item.Metadata)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("adding item %q: %w", item.ID, err)
	}
	// Rollback is a no-op once Commit succeeds (it reports sql.ErrTxDone, which
	// is ignored); a genuine rollback failure is joined so it is not swallowed.
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rolling back add tx: %w", rbErr))
		}
	}()

	if err := enforceDimension(ctx, tx, len(item.Vector)); err != nil {
		return fmt.Errorf("adding item %q: %w", item.ID, err)
	}
	const upsert = `INSERT INTO items (id, vector, metadata) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET vector = excluded.vector, metadata = excluded.metadata`
	if _, err := tx.ExecContext(ctx, upsert, item.ID, blob, metadata); err != nil {
		return fmt.Errorf("adding item %q: %w", item.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("adding item %q: %w", item.ID, err)
	}
	if err := s.ensureWALSiblingsSecured(); err != nil {
		return fmt.Errorf("adding item %q: %w", item.ID, err)
	}
	return nil
}

// enforceDimension pins the store's fixed vector dimension and rejects a
// mismatch. It is idempotent and safe against a concurrent first writer: the
// first writer's insert sets the dimension and a racing writer's insert no-ops,
// after which both read back the winning value and reject dim if it differs.
// Correctness relies on writers serializing (the DSN opens transactions with an
// immediate write lock).
func enforceDimension(ctx context.Context, tx *sql.Tx, dim int) error {
	const insert = `INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`
	if _, err := tx.ExecContext(ctx, insert, metaDimKey, strconv.Itoa(dim)); err != nil {
		return fmt.Errorf("recording dimension: %w", err)
	}
	stored, ok, err := readDimension(ctx, tx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("recording dimension: %w", errDimensionReadBack)
	}
	if dim != stored {
		return fmt.Errorf("%w: got %d, want %d", vectorstore.ErrDimensionMismatch, dim, stored)
	}
	return nil
}

// dimensionReader is the subset of query behavior shared by *sql.DB and *sql.Tx
// used to read the persisted dimension, so enforceDimension (tx) and Nearest
// (db) can share one accessor.
type dimensionReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rowQuerier is the subset of *sql.DB/*sql.Tx used to scan stored items, so the
// item scan can run inside the same read-only snapshot transaction as the
// dimension read.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readDimension returns the persisted store dimension and whether one is set.
func readDimension(ctx context.Context, r dimensionReader) (dim int, ok bool, err error) {
	const query = `SELECT value FROM meta WHERE key = ?`
	switch scanErr := r.QueryRowContext(ctx, query, metaDimKey).Scan(&dim); {
	case errors.Is(scanErr, sql.ErrNoRows):
		return 0, false, nil
	case scanErr != nil:
		return 0, false, fmt.Errorf("reading dimension: %w", scanErr)
	default:
		return dim, true, nil
	}
}

// Nearest returns up to limit neighbors ordered by descending cosine
// similarity, breaking ties by ascending Item.ID. Returned items are fresh
// decodes and therefore independent copies. It returns [vectorstore.ErrInvalidLimit]
// when limit is not positive.
func (s *Store) Nearest(ctx context.Context, vector vectorstore.Vector, limit int) (neighbors []vectorstore.Neighbor, err error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("querying nearest: %w", err)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("querying nearest: %w: %d", vectorstore.ErrInvalidLimit, limit)
	}
	// Normalize validates the query (empty, non-finite, zero magnitude) up front
	// so an invalid query fails before any row scan, matching MemoryStore. The
	// normalized query is captured and reused to score every row, so the query
	// is normalized once rather than re-normalized per row.
	normQuery, err := vectorstore.Normalize(vector)
	if err != nil {
		return nil, fmt.Errorf("querying nearest: %w", err)
	}

	// Read the dimension and every row from ONE read-only snapshot so a
	// concurrent first Add cannot commit a differently sized item between the
	// dimension check and the row scan (which would otherwise surface a spurious
	// ErrCorruptVector). A ReadOnly tx begins deferred even under
	// _txlock=immediate: it takes no write lock, so readers still run
	// concurrently with writers while observing a single consistent snapshot.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("querying nearest: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("querying nearest: closing read snapshot: %w", rbErr))
		}
	}()

	if stored, ok, derr := readDimension(ctx, tx); derr != nil {
		return nil, fmt.Errorf("querying nearest: %w", derr)
	} else if ok && len(vector) != stored {
		return nil, fmt.Errorf("querying nearest: %w: got %d, want %d", vectorstore.ErrDimensionMismatch, len(vector), stored)
	}

	neighbors, err = scanItems(ctx, tx, normQuery)
	if err != nil {
		return nil, err
	}
	// Rows are fully materialized; release the snapshot before the in-memory sort.
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("querying nearest: %w", err)
	}

	sort.Slice(neighbors, func(i, j int) bool {
		if neighbors[i].Score != neighbors[j].Score {
			return neighbors[i].Score > neighbors[j].Score
		}
		return neighbors[i].Item.ID < neighbors[j].Item.ID
	})
	if len(neighbors) > limit {
		neighbors = neighbors[:limit]
	}
	return neighbors, nil
}

// scanItems reads and scores every stored item against the pre-normalized query.
// It checks ctx in the loop so cancellation is honored even on an empty store.
// Each stored vector is normalized once and scored via vectorstore.CosineUnit,
// the unit-length fast path, so the query is not re-normalized per row. The
// returned Item.Vector stays the raw decoded vector; only scoring uses the
// normalized copy, so the stored form is never persisted normalized.
func scanItems(ctx context.Context, q rowQuerier, normQuery vectorstore.Vector) (neighbors []vectorstore.Neighbor, err error) {
	const query = `SELECT id, vector, metadata FROM items`
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying nearest: %w", err)
	}
	// Join a rows.Close failure with any primary error so it is not swallowed.
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("querying nearest: closing rows: %w", cerr))
		}
	}()

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("querying nearest: scan cancelled: %w", err)
		}
		var (
			id       string
			blob     []byte
			metadata []byte
		)
		if err := rows.Scan(&id, &blob, &metadata); err != nil {
			return nil, fmt.Errorf("querying nearest: %w", err)
		}
		vec, err := decodeVector(blob)
		if err != nil {
			return nil, fmt.Errorf("querying nearest: item %q: %w", id, err)
		}
		meta, err := decodeMetadata(metadata)
		if err != nil {
			return nil, fmt.Errorf("querying nearest: item %q: %w", id, err)
		}
		nv, err := vectorstore.Normalize(vec)
		if err != nil {
			// A stored vector that fails validation is corrupt persisted data
			// (Add validates before persisting), not an invalid caller query,
			// so surface ErrCorruptVector instead of leaking ErrEmptyVector or
			// ErrInvalidVector as though the query were at fault. The reason is
			// formatted with %s (not %w) so ErrInvalidVector stays out of the
			// error chain and cannot be mistaken for a caller-input fault.
			return nil, fmt.Errorf("querying nearest: item %q: %w: %s", id, ErrCorruptVector, err.Error())
		}
		// A stored vector can decode and normalize cleanly yet still carry a
		// dimension different from the store's (byte-aligned but wrong length).
		// Add enforces the store dimension, so that is corrupt persisted data,
		// not a bad caller query; detect it here and surface ErrCorruptVector so
		// CosineUnit cannot leak ErrDimensionMismatch as though the already
		// validated query were at fault. normQuery's length equals the store
		// dimension (validated in Nearest before scan).
		if len(nv) != len(normQuery) {
			return nil, fmt.Errorf("querying nearest: item %q: %w: dimension %d, want %d", id, ErrCorruptVector, len(nv), len(normQuery))
		}
		score, err := vectorstore.CosineUnit(normQuery, nv)
		if err != nil {
			return nil, fmt.Errorf("querying nearest: scoring item %q: %w", id, err)
		}
		neighbors = append(neighbors, vectorstore.Neighbor{
			Item:  vectorstore.Item{ID: id, Vector: vec, Metadata: meta},
			Score: score,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying nearest: %w", err)
	}
	// An empty store still must honor a canceled context, mirroring MemoryStore,
	// where an empty scan loop would otherwise never observe cancellation.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("querying nearest: scan cancelled: %w", err)
	}
	return neighbors, nil
}

// encodeMetadata serializes m with a byte-preserving, deterministic layout so
// arbitrary caller bytes (including invalid UTF-8) round-trip verbatim, matching
// [vectorstore.MemoryStore]. A nil map stays absent (NULL) and decodes back to a
// nil map; a non-nil (possibly empty) map encodes to a non-empty blob. JSON is
// unusable here because it replaces invalid UTF-8 with U+FFFD.
func encodeMetadata(m map[string]string) []byte {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := binary.AppendUvarint(nil, uint64(len(m)))
	for _, k := range keys {
		v := m[k]
		buf = binary.AppendUvarint(buf, uint64(len(k)))
		buf = append(buf, k...)
		buf = binary.AppendUvarint(buf, uint64(len(v)))
		buf = append(buf, v...)
	}
	return buf
}

// decodeMetadata reverses encodeMetadata. A nil (SQL NULL) blob decodes to a nil
// map. Any other malformed blob (bad length prefix, truncated field, or trailing
// bytes) is rejected with [ErrCorruptMetadata]; encodeMetadata only ever emits
// NULL or a well-formed blob.
func decodeMetadata(b []byte) (map[string]string, error) {
	if b == nil {
		return nil, nil
	}
	count, n := binary.Uvarint(b)
	if n <= 0 {
		return nil, fmt.Errorf("%w: bad entry count", ErrCorruptMetadata)
	}
	rest := b[n:]
	// Do NOT presize the map from the untrusted count; a corrupt count could
	// request a huge allocation. Entries grow the map as they are parsed.
	m := make(map[string]string)
	for i := uint64(0); i < count; i++ {
		var key, val []byte
		var err error
		if key, rest, err = readMetadataField(rest); err != nil {
			return nil, err
		}
		if val, rest, err = readMetadataField(rest); err != nil {
			return nil, err
		}
		m[string(key)] = string(val)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("%w: %d trailing bytes", ErrCorruptMetadata, len(rest))
	}
	return m, nil
}

// readMetadataField reads one uvarint-length-prefixed byte field from b and
// returns the field and the remaining bytes.
func readMetadataField(b []byte) (field, rest []byte, err error) {
	length, n := binary.Uvarint(b)
	if n <= 0 {
		return nil, nil, fmt.Errorf("%w: bad field length", ErrCorruptMetadata)
	}
	b = b[n:]
	if uint64(len(b)) < length {
		return nil, nil, fmt.Errorf("%w: field length %d exceeds %d remaining bytes", ErrCorruptMetadata, length, len(b))
	}
	return b[:length], b[length:], nil
}
