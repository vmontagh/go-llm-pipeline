package pipeline

import (
	"context"
	"fmt"
)

// Event defines the contract for any event flowing through the pipeline.
type Event interface {
	EventType() string
	ItemID() string
	Message() string
	IsError() bool
	IsVerbose() bool
}

// Logger defines the consumer interface for logging.
type Logger interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Errorf(format string, v ...any)
}

// EventLogger consumes the event stream and writes structured logs.
type EventLogger struct {
	appLogger Logger
}

// NewEventLogger creates a new EventLogger.
func NewEventLogger(appLogger Logger) *EventLogger {
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
				l.logEvent(event)
			}
		}
	}()
}

func (l *EventLogger) logEvent(event Event) {
	msg := fmt.Sprintf("[%s] ItemID=%s: %s", event.EventType(), event.ItemID(), event.Message())

	switch {
	case event.IsError():
		l.appLogger.Errorf("%s", msg)
	case event.IsVerbose():
		l.appLogger.Debugf("%s", msg)
	default:
		l.appLogger.Infof("%s", msg)
	}
}
