package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	dirPermissions  = 0o755
	filePermissions = 0o644
)

// DLQLogger defines the consumer interface for logging in the DLQ.
type DLQLogger interface {
	Errorf(format string, v ...any)
}

// DeadLetterQueue listens for failed events and writes them to a JSONL file.
type DeadLetterQueue struct {
	filePath string
	logger   DLQLogger

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

// NewDeadLetterQueue creates a new DLQ that appends to the specified file.
func NewDeadLetterQueue(filePath string, appLogger DLQLogger) (*DeadLetterQueue, error) {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, dirPermissions); err != nil {
		return nil, fmt.Errorf("creating DLQ directory: %w", err)
	}

	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePermissions)
	if err != nil {
		return nil, fmt.Errorf("opening DLQ file: %w", err)
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
		d.logger.Errorf("Failed to write to DLQ for ItemID=%s: %v", itemID, err)
	}
}
