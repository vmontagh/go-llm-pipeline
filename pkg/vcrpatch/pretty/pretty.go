package pretty

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/vmontagh/go-llm-pipeline/pkg/vcrpatch"
)

const (
	headerContentType   = "Content-Type"
	headerContentLength = "Content-Length"
	contentTypeJSON     = "application/json"
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
func FormatInteraction(i *vcrpatch.Interaction) bool {
	requestChanged := formatRequest(i)
	responseChanged := formatResponse(i)
	return requestChanged || responseChanged
}

func formatRequest(i *vcrpatch.Interaction) bool {
	prettyReq, updated := formatBody(i.Request.Body, i.Request.Headers, i.Request.ContentLength)
	if !updated {
		return false
	}
	i.Request.Body = prettyReq
	i.Request.ContentLength = len(prettyReq)
	return true
}

func formatResponse(i *vcrpatch.Interaction) bool {
	prettyResp, updated := formatBody(i.Response.Body, i.Response.Headers, i.Response.ContentLength)
	if !updated {
		return false
	}
	i.Response.Body = prettyResp
	i.Response.ContentLength = len(prettyResp)
	return true
}

func formatBody(body string, headers map[string][]string, contentLength int) (string, bool) {
	if !isJSONContent(headers) {
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

func isJSONContent(headers map[string][]string) bool {
	values := headers[headerContentType]
	return len(values) > 0 && strings.Contains(values[0], contentTypeJSON)
}

// FormatCassette reads a cassette file, pretty-prints its JSON interactions, and saves it.
func FormatCassette(path string) (bool, error) {
	c, err := vcrpatch.LoadCassette(path)
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
	if err := vcrpatch.SaveCassette(path, c); err != nil {
		return false, fmt.Errorf("saving %s: %w", path, err)
	}
	return true, nil
}

// PrintPrettyResult prints formatting results to an output stream.
func PrintPrettyResult(w io.Writer, path string, changed bool) {
	msg := msgNoChanges
	if changed {
		msg = msgFormattedJSON
	}
	fmt.Fprintf(w, msg, path)
}
