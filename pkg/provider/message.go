package provider

import (
	"context"
	"errors"
	"fmt"
)

// unknownName names an invalid enum value.
const unknownName = "unknown"

// Role is the speaker of a Message.
type Role int

const (
	// RoleUnknown is the zero value and is never valid.
	RoleUnknown Role = iota
	// RoleSystem carries instructions for the model.
	RoleSystem
	// RoleUser is a turn written by the caller.
	RoleUser
	// RoleAssistant is an earlier model reply.
	RoleAssistant
)

// String returns the lowercase role name.
func (r Role) String() string {
	switch r {
	case RoleSystem:
		return "system"
	case RoleUser:
		return "user"
	case RoleAssistant:
		return "assistant"
	default:
		return unknownName
	}
}

// Part is one piece of message content: either Text or Image, never both.
type Part struct {
	// Text is a text segment.
	Text string
	// Image is raw JPEG image data.
	Image []byte
}

// Message is one conversation turn.
type Message struct {
	Role  Role
	Parts []Part
}

// TextMessage returns a message with a single text part.
func TextMessage(role Role, text string) Message {
	return Message{Role: role, Parts: []Part{{Text: text}}}
}

// ErrInvalidMessages is returned for a message list that fails
// ValidateMessages.
var ErrInvalidMessages = errors.New("invalid messages")

// ErrMessagesUnsupported is returned when a provider cannot take typed
// messages.
var ErrMessagesUnsupported = errors.New("provider does not support messages")

// ValidateMessages reports every problem in msgs, joined. It checks that
// every role is known, every part holds exactly one of Text or Image, system
// messages are text only, and at least one message is a user or assistant
// turn.
func ValidateMessages(msgs []Message) error {
	var errs []error
	conversation := false
	for i, m := range msgs {
		switch m.Role {
		case RoleSystem, RoleUser, RoleAssistant:
		default:
			errs = append(errs, fmt.Errorf("%w: message %d has role %v", ErrInvalidMessages, i, m.Role))
		}
		if len(m.Parts) == 0 {
			errs = append(errs, fmt.Errorf("%w: message %d has no parts", ErrInvalidMessages, i))
		}
		for j, p := range m.Parts {
			if (p.Text == "") == (len(p.Image) == 0) {
				errs = append(errs, fmt.Errorf("%w: message %d part %d must hold exactly one of text or image", ErrInvalidMessages, i, j))
			}
			if m.Role == RoleSystem && len(p.Image) > 0 {
				errs = append(errs, fmt.Errorf("%w: system message %d has an image", ErrInvalidMessages, i))
			}
		}
		conversation = conversation || m.Role == RoleUser || m.Role == RoleAssistant
	}
	if !conversation {
		errs = append(errs, fmt.Errorf("%w: no user or assistant message", ErrInvalidMessages))
	}
	return errors.Join(errs...)
}

// AsMessageCaller returns p as a MessageCaller when it takes typed messages.
// A Guard counts only if the provider it wraps does, though a Guard always
// has CallMessages.
func AsMessageCaller[T any](p Provider[T]) (MessageCaller[T], bool) {
	inner := p
	if g, ok := p.(*Guard[T]); ok {
		inner = g.Provider
	}
	if _, ok := inner.(MessageCaller[T]); !ok {
		return nil, false
	}
	mc, ok := p.(MessageCaller[T])
	return mc, ok
}

// MessageCaller is implemented by providers that accept typed multi-turn
// messages. msgs has passed ValidateMessages. The lease rules of
// Provider.Call apply.
type MessageCaller[T any] interface {
	CallMessages(ctx context.Context, lease *Lease[T], msgs []Message) CallResult
}
