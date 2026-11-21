package sqlitestore

import (
	"errors"
	"fmt"
	"testing"

	sqlitelib "modernc.org/sqlite/lib"
)

var errNotSQLite = errors.New("not a sqlite error")

func TestIsBusyCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int
		want bool
	}{
		{name: "busy", code: sqlitelib.SQLITE_BUSY, want: true},
		{name: "busy recovery", code: sqlitelib.SQLITE_BUSY_RECOVERY, want: true},
		{name: "busy snapshot", code: sqlitelib.SQLITE_BUSY_SNAPSHOT, want: true},
		{name: "busy timeout", code: sqlitelib.SQLITE_BUSY_TIMEOUT, want: true},
		{name: "ok", code: sqlitelib.SQLITE_OK},
		{name: "locked", code: sqlitelib.SQLITE_LOCKED},
		{name: "locked shared cache", code: sqlitelib.SQLITE_LOCKED_SHAREDCACHE},
		{name: "ioerr", code: sqlitelib.SQLITE_IOERR},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isBusyCode(tt.code); got != tt.want {
				t.Errorf("isBusyCode(%d) = %t, want %t", tt.code, got, tt.want)
			}
		})
	}
}

func TestIsSQLiteBusyNonDriverError(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("pinging: %w", errNotSQLite)
	if isSQLiteBusy(err) {
		t.Errorf("isSQLiteBusy(%v) = true, want false", err)
	}
}
