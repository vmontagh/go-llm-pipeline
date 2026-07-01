package vcrpatch

import (
	"errors"
	"fmt"
	"strconv"
)

const patchHeaderContentLength = "Content-Length"

var ErrIndexOutOfBounds = errors.New("interaction index out of bounds")

// PatchInteractionBody replaces the body of a request or response in a cassette by index.
func PatchInteractionBody(path string, index int, isRequest bool, newBody string) (string, error) {
	c, err := LoadCassette(path)
	if err != nil {
		return "", fmt.Errorf("loading %s: %w", path, err)
	}
	if index < 0 || index >= len(c.Interactions) {
		return "", fmt.Errorf("index %d out of bounds (len %d): %w", index, len(c.Interactions), ErrIndexOutOfBounds)
	}

	if isRequest {
		patchRequest(&c.Interactions[index], newBody)
	}
	if !isRequest {
		patchResponse(&c.Interactions[index], newBody)
	}

	if err := SaveCassette(path, c); err != nil {
		return "", fmt.Errorf("saving %s: %w", path, err)
	}
	return path, nil
}

func patchRequest(i *Interaction, newBody string) {
	i.Request.Body = newBody
	i.Request.ContentLength = len(newBody)
	if i.Request.Headers == nil {
		i.Request.Headers = make(map[string][]string)
	}
	i.Request.Headers[patchHeaderContentLength] = []string{strconv.Itoa(len(newBody))}
}

func patchResponse(i *Interaction, newBody string) {
	i.Response.Body = newBody
	i.Response.ContentLength = len(newBody)
	if i.Response.Headers == nil {
		i.Response.Headers = make(map[string][]string)
	}
	i.Response.Headers[patchHeaderContentLength] = []string{strconv.Itoa(len(newBody))}
}
