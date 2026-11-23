package provider

import (
	"context"
	"errors"
	"iter"
)

var (
	// ErrStreamUnsupported is returned for a stream request to a provider
	// that is not a Streamer.
	ErrStreamUnsupported = errors.New("provider does not support streaming")
	// ErrIncompleteStream is returned when a stream ends before the provider
	// marks the reply done.
	ErrIncompleteStream = errors.New("stream ended before the reply was done")
)

// Usage is the token usage a provider reports for one reply.
type Usage struct {
	Input  int
	Output int
}

// Chunk is one piece of a streamed reply.
type Chunk struct {
	Text string
	// Usage is set on the last chunk when the provider reports it.
	Usage *Usage
}

// Streamer is implemented by providers that can stream a reply to typed
// messages. msgs has passed ValidateMessages. The lease rules of
// Provider.Call apply.
type Streamer[T any] interface {
	// Stream sends msgs and opens the reply. A failure to open is reported
	// in the CallResult like a failed Call, with a nil iterator. On
	// OutcomeSuccess the caller must range over the iterator once: it frees
	// the connection when ranging stops.
	Stream(ctx context.Context, lease *Lease[T], msgs []Message) (iter.Seq2[Chunk, error], CallResult)
}

// AsStreamer returns p as a Streamer when it can stream. Guards count only
// if the provider they wrap does, though a Guard always has Stream.
func AsStreamer[T any](p Provider[T]) (Streamer[T], bool) {
	return as[Streamer[T]](p)
}

// as returns p as C when p, or the provider under all of p's Guards,
// implements C.
func as[C any, T any](p Provider[T]) (C, bool) {
	var zero C
	inner := p
	for g, ok := inner.(*Guard[T]); ok; g, ok = inner.(*Guard[T]) {
		inner = g.Provider
	}
	if _, ok := inner.(C); !ok {
		return zero, false
	}
	c, ok := p.(C)
	return c, ok
}
