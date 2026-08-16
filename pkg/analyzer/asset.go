package analyzer

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// Asset is the domain-agnostic unit of work consumed by an analysis pipeline.
// The same contract carries binary media (for example, .DNG or .ARW raws),
// plain-text documents (for example, .txt or .md), and structured payloads (for
// example, .json), so a single pipeline can process heterogeneous inputs
// without vertical-specific coupling.
type Asset struct {
	// ID is a stable unique identifier for the asset, such as a URI, filename,
	// or external ID. It correlates published events and log entries.
	ID string
	// Payload holds the raw asset bytes consumed by the pipeline. It may be nil
	// for prompt-only analyses that carry no binary payload.
	Payload io.Reader
	// Metadata carries provider-agnostic attributes that are not part of the raw
	// payload bytes, such as a content type or source annotations. It may be nil.
	Metadata map[string]any
}

// AssetFromBytes returns an Asset whose Payload reads from b.
func AssetFromBytes(id string, b []byte, metadata map[string]any) Asset {
	return Asset{ID: id, Payload: bytes.NewReader(b), Metadata: metadata}
}

// AssetFromString returns an Asset whose Payload reads from s.
func AssetFromString(id, s string, metadata map[string]any) Asset {
	return Asset{ID: id, Payload: strings.NewReader(s), Metadata: metadata}
}

// AssetFromReader returns an Asset whose Payload reads from r.
func AssetFromReader(id string, r io.Reader, metadata map[string]any) Asset {
	return Asset{ID: id, Payload: r, Metadata: metadata}
}

// payloadBytes reads the entire payload into memory. A nil Payload yields a nil
// slice and no error. The Payload is consumed and cannot be read again.
func (a Asset) payloadBytes() ([]byte, error) {
	if a.Payload == nil {
		return nil, nil
	}
	b, err := io.ReadAll(a.Payload)
	if err != nil {
		return nil, fmt.Errorf("reading asset payload %q: %w", a.ID, err)
	}
	return b, nil
}
