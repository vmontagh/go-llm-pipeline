package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vmontagh/go-llm-pipeline/internal/appendfile"
)

const (
	dirPermissions  = 0o700
	filePermissions = 0o600
)

// DeadLetterQueue listens for failed events and writes them to a JSONL file.
type DeadLetterQueue struct {
	filePath string
	logger   *slog.Logger

	mu      sync.Mutex
	file    *os.File
	encoder *json.Encoder
}

// DLQEntry represents a single error entry in the dead letter queue.
type DLQEntry struct {
	Timestamp time.Time `json:"timestamp"`
	ItemID    string    `json:"item_id"`
	Stage     string    `json:"stage"`
	Error     string    `json:"error"`
}

// NewDeadLetterQueue creates a new DLQ that appends to the specified file. The
// path must be a regular file or not yet exist; a directory, FIFO or device is
// rejected. On Unix a final-component symlink is rejected too, and the check
// never blocks on a FIFO or device.
func NewDeadLetterQueue(filePath string, appLogger *slog.Logger) (*DeadLetterQueue, error) {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, dirPermissions); err != nil {
		return nil, fmt.Errorf("creating DLQ directory: %w", err)
	}

	file, err := appendfile.Open(filePath, filePermissions)
	if err != nil {
		return nil, fmt.Errorf("opening DLQ file: %w", err)
	}

	if appLogger == nil {
		appLogger = slog.New(slog.DiscardHandler)
	}

	return &DeadLetterQueue{
		filePath: filePath,
		file:     file,
		encoder:  json.NewEncoder(file),
		logger:   appLogger,
	}, nil
}

// Close closes the underlying file handle.
func (d *DeadLetterQueue) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file != nil {
		return d.file.Close()
	}
	return nil
}

// Record appends a failed event to the dead letter queue.
func (d *DeadLetterQueue) Record(timestamp time.Time, itemID, stage string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	entry := DLQEntry{
		Timestamp: timestamp,
		ItemID:    itemID,
		Stage:     stage,
		Error:     errMsg,
	}

	if err := d.encoder.Encode(entry); err != nil {
		d.logger.Error("writing to DLQ failed", "itemID", itemID, "err", err)
	}
}
