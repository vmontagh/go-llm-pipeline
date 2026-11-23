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
	// RoleTool carries the results of the tool calls in the assistant
	// message before it.
	RoleTool
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
	case RoleTool:
		return "tool"
	default:
		return unknownName
	}
}

// Part is one piece of message content. It holds exactly one field.
type Part struct {
	// Text is a text segment.
	Text string
	// Image is raw JPEG image data, allowed in user messages only.
	Image []byte
	// ToolCall is a tool request, allowed in assistant messages only.
	ToolCall *ToolCall
	// ToolResult is a tool outcome, the only part a tool message may hold.
	ToolResult *ToolResult
	// Signature is opaque provider state on a model's part that must go back
	// with it, such as Gemini's thought signature. Keep it as received;
	// providers without such state ignore it.
	Signature string
}

// fields returns how many of p's content fields are set.
func (p Part) fields() int {
	n := 0
	for _, set := range []bool{p.Text != "", len(p.Image) > 0, p.ToolCall != nil, p.ToolResult != nil} {
		if set {
			n++
		}
	}
	return n
}

// HasTools reports whether any message holds a tool call or result.
func HasTools(msgs []Message) bool {
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.ToolCall != nil || p.ToolResult != nil {
				return true
			}
		}
	}
	return false
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
// every role is known, every part holds exactly one field, each part kind is
// in a role that allows it, tool calls and results are named, and at least
// one message is a user or assistant turn.
func ValidateMessages(msgs []Message) error {
	var errs []error
	conversation := false
	for i, m := range msgs {
		switch m.Role {
		case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		default:
			errs = append(errs, fmt.Errorf("%w: message %d has role %v", ErrInvalidMessages, i, m.Role))
		}
		if len(m.Parts) == 0 {
			errs = append(errs, fmt.Errorf("%w: message %d has no parts", ErrInvalidMessages, i))
		}
		for j, p := range m.Parts {
			if err := validatePart(m.Role, p); err != nil {
				errs = append(errs, fmt.Errorf("%w: %v message %d part %d %w", ErrInvalidMessages, m.Role, i, j, err))
			}
		}
		conversation = conversation || m.Role == RoleUser || m.Role == RoleAssistant
	}
	if !conversation {
		errs = append(errs, fmt.Errorf("%w: no user or assistant message", ErrInvalidMessages))
	}
	return errors.Join(errs...)
}

// Part problems, reported by ValidateMessages.
var (
	errPartFields  = errors.New("must hold exactly one field")
	errPartRole    = errors.New("is not allowed in this role")
	errToolUnnamed = errors.New("has no tool name")
)

// validatePart checks that p holds one field that role allows.
func validatePart(role Role, p Part) error {
	switch {
	case p.fields() != 1:
		return errPartFields
	case p.Text != "" && role == RoleTool,
		len(p.Image) > 0 && role != RoleUser,
		p.ToolCall != nil && role != RoleAssistant,
		p.ToolResult != nil && role != RoleTool:
		return errPartRole
	case p.ToolCall != nil && p.ToolCall.Name == "",
		p.ToolResult != nil && p.ToolResult.Name == "":
		return errToolUnnamed
	}
	return nil
}

// AsMessageCaller returns p as a MessageCaller when it takes typed messages.
// Guards count only if the provider they wrap does, though a Guard always
// has CallMessages.
func AsMessageCaller[T any](p Provider[T]) (MessageCaller[T], bool) {
	return as[MessageCaller[T]](p)
}

// MessageCaller is implemented by providers that accept typed multi-turn
// messages. msgs has passed ValidateMessages. The lease rules of
// Provider.Call apply.
type MessageCaller[T any] interface {
	CallMessages(ctx context.Context, lease *Lease[T], msgs []Message) CallResult
}
