package analyzer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MetadataContentType is the Asset.Metadata key that carries the payload media
// type (for example, "image/jpeg", "text/plain", or "application/json").
// AnalyzeAsset consults it to route the payload: textual media is folded into
// the prompt, while any other media (including images) is forwarded as binary
// provider data.
const MetadataContentType = "contentType"

// DefaultMaxAssetBytes is the default upper bound AnalyzeAsset applies when
// reading an Asset payload into memory. It guards concurrent pipeline workers
// against unbounded or non-terminating streams. Override it with
// WithMaxAssetBytes; a value of zero disables the bound.
const DefaultMaxAssetBytes int64 = 64 << 20

// ErrPayloadTooLarge is returned when an Asset payload exceeds the configured
// maximum size. Callers can match it with errors.Is.
var ErrPayloadTooLarge = errors.New("analyzer: asset payload exceeds maximum size")

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
	// payload bytes, such as the payload media type (see MetadataContentType) or
	// source annotations. It may be nil.
	Metadata map[string]any
}

// AssetFromBytes returns an Asset whose Payload reads from b.
func AssetFromBytes(id string, b []byte, metadata map[string]any) Asset {
	return Asset{ID: id, Payload: bytes.NewReader(b), Metadata: metadata}
}

// AssetFromString returns an Asset whose Payload reads from s. Unless metadata
// already sets MetadataContentType, the asset is tagged as "text/plain" so
// AnalyzeAsset folds it into the prompt rather than sending it as binary data.
func AssetFromString(id, s string, metadata map[string]any) Asset {
	return Asset{
		ID:       id,
		Payload:  strings.NewReader(s),
		Metadata: withDefaultContentType(metadata, "text/plain; charset=utf-8"),
	}
}

// AssetFromReader returns an Asset whose Payload reads from r.
func AssetFromReader(id string, r io.Reader, metadata map[string]any) Asset {
	return Asset{ID: id, Payload: r, Metadata: metadata}
}

// contentType returns the media type recorded under MetadataContentType, or the
// empty string when absent.
func (a Asset) contentType() string {
	ct, ok := a.Metadata[MetadataContentType].(string)
	if !ok {
		return ""
	}
	return ct
}

// payloadBytes reads the payload into memory, honoring ctx cancellation between
// reads and enforcing limit (in bytes; zero means unbounded). A nil Payload
// yields a nil slice and no error. The Payload is consumed and cannot be read
// again.
func (a Asset) payloadBytes(ctx context.Context, limit int64) ([]byte, error) {
	if a.Payload == nil {
		return nil, nil
	}
	b, err := readAll(ctx, a.Payload, limit)
	if err != nil {
		return nil, fmt.Errorf("reading asset payload %q: %w", a.ID, err)
	}
	return b, nil
}

// readAll reads r into memory in chunks, returning ctx.Err() if the context is
// canceled between reads and ErrPayloadTooLarge if the byte count exceeds a
// non-zero limit.
func readAll(ctx context.Context, r io.Reader, limit int64) ([]byte, error) {
	const chunkSize = 32 * 1024
	var (
		out   []byte
		total int64
		chunk [chunkSize]byte
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := r.Read(chunk[:])
		if n > 0 {
			total += int64(n)
			if limit > 0 && total > limit {
				return nil, fmt.Errorf("%w: %d bytes", ErrPayloadTooLarge, limit)
			}
			out = append(out, chunk[:n]...)
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// isTextualContentType reports whether ct denotes text or JSON media that should
// be folded into the prompt rather than sent as binary provider data.
func isTextualContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch {
	case strings.HasPrefix(ct, "text/"):
		return true
	case ct == "application/json", strings.HasSuffix(ct, "+json"):
		return true
	default:
		return false
	}
}

// withDefaultContentType returns metadata with MetadataContentType set to ct
// when it is absent. The caller's map is never mutated.
func withDefaultContentType(metadata map[string]any, ct string) map[string]any {
	if _, ok := metadata[MetadataContentType]; ok {
		return metadata
	}
	out := make(map[string]any, len(metadata)+1)
	for k, v := range metadata {
		out[k] = v
	}
	out[MetadataContentType] = ct
	return out
}
