package pipeline

import (
	"context"
	"fmt"
	"log/slog"
)

// Event defines the contract for any event flowing through the pipeline.
type Event interface {
	EventType() string
	ItemID() string
	Message() string
	IsError() bool
	IsVerbose() bool
}

// EventLogger consumes the event stream and writes structured logs.
type EventLogger struct {
	appLogger *slog.Logger
}

// NewEventLogger creates a new EventLogger.
func NewEventLogger(appLogger *slog.Logger) *EventLogger {
	if appLogger == nil {
		appLogger = slog.New(slog.DiscardHandler)
	}
	return &EventLogger{
		appLogger: appLogger,
	}
}

// Start spawns a background goroutine to consume events.
func (l *EventLogger) Start(ctx context.Context, events <-chan Event) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				l.logEvent(ctx, event)
			}
		}
	}()
}

func (l *EventLogger) logEvent(ctx context.Context, event Event) {
	msg := fmt.Sprintf("[%s] ItemID=%s: %s", event.EventType(), event.ItemID(), event.Message())

	switch {
	case event.IsError():
		l.appLogger.ErrorContext(ctx, msg)
	case event.IsVerbose():
		l.appLogger.DebugContext(ctx, msg)
	default:
		l.appLogger.InfoContext(ctx, msg)
	}
}
