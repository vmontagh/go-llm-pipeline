package llmvcr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

const (
	headerContentLength = "Content-Length"
	jsonIndentNone      = ""
	jsonIndentPadding   = "  "
	msgFormattedJSON    = "Formatted JSON in %s\n"
	msgNoChanges        = "No changes for %s\n"
)

// FormatJSON pretty-prints a raw JSON string.
func FormatJSON(raw string) string {
	if raw == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), jsonIndentNone, jsonIndentPadding); err == nil {
		return buf.String()
	}
	return raw
}

// FormatInteraction pretty-prints request/response JSON bodies in an interaction.
func FormatInteraction(i *Interaction) bool {
	if i == nil {
		return false
	}
	requestChanged := formatRequest(i)
	responseChanged := formatResponse(i)
	return requestChanged || responseChanged
}

func formatRequest(i *Interaction) bool {
	prettyReq, updated := formatBody(i.Request.Body, i.Request.Headers, i.Request.ContentLength)
	if !updated {
		return false
	}
	i.Request.Body = prettyReq
	i.Request.ContentLength = len(prettyReq)
	return true
}

func formatResponse(i *Interaction) bool {
	prettyResp, updated := formatBody(i.Response.Body, i.Response.Headers, i.Response.ContentLength)
	if !updated {
		return false
	}
	i.Response.Body = prettyResp
	i.Response.ContentLength = len(prettyResp)
	return true
}

func formatBody(body string, headers map[string][]string, contentLength int) (string, bool) {
	if !hasJSONContentType(headers) {
		return body, false
	}
	prettyBody := FormatJSON(body)
	if body == prettyBody && contentLength == len(prettyBody) {
		return body, false
	}
	if len(headers[headerContentLength]) > 0 {
		headers[headerContentLength][0] = strconv.Itoa(len(prettyBody))
	}
	return prettyBody, true
}

// FormatCassette reads a cassette file, pretty-prints its JSON interactions, and saves it.
func FormatCassette(path string) (bool, error) {
	c, err := LoadCassette(path)
	if err != nil {
		return false, fmt.Errorf("loading %s: %w", path, err)
	}

	changed := false
	for idx := range c.Interactions {
		if FormatInteraction(&c.Interactions[idx]) {
			changed = true
		}
	}

	if !changed {
		return false, nil
	}
	if err := SaveCassette(path, c); err != nil {
		return false, fmt.Errorf("saving %s: %w", path, err)
	}
	return true, nil
}

// PrintPrettyResult prints formatting results to an output stream.
func PrintPrettyResult(w io.Writer, path string, changed bool) error {
	msg := msgNoChanges
	if changed {
		msg = msgFormattedJSON
	}
	if _, err := fmt.Fprintf(w, msg, path); err != nil {
		return fmt.Errorf("writing pretty result: %w", err)
	}
	return nil
}
