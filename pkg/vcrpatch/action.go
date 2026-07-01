package vcrpatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Action defines the contract to mutate an interaction response or inject interactions.
type Action interface {
	Apply(matched *Interaction, allInteractions []Interaction) (replacement []Interaction, err error)
}

type replaceStatusCodeAction struct {
	code int
}

func (a replaceStatusCodeAction) Apply(matched *Interaction, _ []Interaction) ([]Interaction, error) {
	matched.Response.Code = a.code
	statusText := http.StatusText(a.code)
	if statusText != "" {
		matched.Response.Status = fmt.Sprintf("%d %s", a.code, statusText)
	} else {
		matched.Response.Status = fmt.Sprintf("%d Status", a.code)
	}
	return []Interaction{*matched}, nil
}

// ReplaceStatusCode returns an action to replace the HTTP status code of the response.
func ReplaceStatusCode(code int) Action {
	return replaceStatusCodeAction{code: code}
}

type replaceBodyAction struct {
	body string
}

func (a replaceBodyAction) Apply(matched *Interaction, _ []Interaction) ([]Interaction, error) {
	matched.Response.Body = a.body
	return []Interaction{*matched}, nil
}

// ReplaceBody returns an action to replace the response body.
func ReplaceBody(body string) Action {
	return replaceBodyAction{body: body}
}

type mutateBodyAction struct {
	fn func(string) (string, error)
}

func (a mutateBodyAction) Apply(matched *Interaction, _ []Interaction) ([]Interaction, error) {
	newBody, err := a.fn(matched.Response.Body)
	if err != nil {
		return nil, err
	}
	matched.Response.Body = newBody
	return []Interaction{*matched}, nil
}

// MutateBody returns an action to mutate the response body via a user-defined function.
func MutateBody(fn func(string) (string, error)) Action {
	return mutateBodyAction{fn: fn}
}

type replaceHeadersAction struct {
	headers map[string][]string
}

func (a replaceHeadersAction) Apply(matched *Interaction, _ []Interaction) ([]Interaction, error) {
	if matched.Response.Headers == nil {
		matched.Response.Headers = make(map[string][]string)
	}
	for k, v := range a.headers {
		matched.Response.Headers[k] = v
	}
	return []Interaction{*matched}, nil
}

// ReplaceHeaders returns an action to replace/add specific headers.
func ReplaceHeaders(headers map[string][]string) Action {
	return replaceHeadersAction{headers: headers}
}

type injectBeforeAction struct {
	interactions []Interaction
}

func (a injectBeforeAction) Apply(matched *Interaction, _ []Interaction) ([]Interaction, error) {
	result := make([]Interaction, 0, len(a.interactions)+1)
	for idx := range a.interactions {
		inject := &a.interactions[idx]
		clone := *matched
		clone.Response.Body = inject.Response.Body
		clone.Response.Code = inject.Response.Code
		clone.Response.Status = inject.Response.Status
		if inject.Response.Headers != nil {
			clone.Response.Headers = inject.Response.Headers
		}
		if inject.Response.TransferEncoding != nil {
			clone.Response.TransferEncoding = inject.Response.TransferEncoding
		}
		clone.Response.Duration = "0s"
		result = append(result, clone)
	}
	result = append(result, *matched)
	return result, nil
}

// InjectBefore returns an action to inject new interactions before the matched interaction.
func InjectBefore(interactions ...Interaction) Action {
	return injectBeforeAction{interactions: interactions}
}

type syncContentLengthAction struct{}

const contentTypeJSON = "application/json"

func (syncContentLengthAction) Apply(matched *Interaction, _ []Interaction) ([]Interaction, error) {
	resp := &matched.Response
	if len(resp.Headers["Content-Type"]) > 0 && strings.Contains(resp.Headers["Content-Type"][0], contentTypeJSON) {
		if resp.Body != "" {
			var buf bytes.Buffer
			if err := json.Indent(&buf, []byte(resp.Body), "", "  "); err == nil {
				resp.Body = buf.String()
			}
		}
	}

	resp.ContentLength = len(resp.Body)
	if resp.Headers == nil {
		resp.Headers = make(map[string][]string)
	}
	resp.Headers["Content-Length"] = []string{strconv.Itoa(resp.ContentLength)}
	delete(resp.Headers, "Transfer-Encoding")
	resp.TransferEncoding = []string{}
	return []Interaction{*matched}, nil
}

// SyncContentLength returns an action to format JSON bodies and sync the Content-Length header.
func SyncContentLength() Action {
	return syncContentLengthAction{}
}

type chainAction struct {
	actions []Action
}

func (c chainAction) Apply(matched *Interaction, all []Interaction) ([]Interaction, error) {
	current := []Interaction{*matched}
	for _, action := range c.actions {
		var next []Interaction
		for idx := range current {
			res, err := action.Apply(&current[idx], all)
			if err != nil {
				return nil, err
			}
			next = append(next, res...)
		}
		current = next
	}
	return current, nil
}

// Chain returns a composite Action that executes a sequence of Actions.
func Chain(actions ...Action) Action {
	return chainAction{actions: actions}
}
